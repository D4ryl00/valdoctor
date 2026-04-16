package source

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCmdSourceStreamsLinesFromCommand(t *testing.T) {
	var gotCmd []string
	src := &CmdSource{
		Source: model.Source{Path: "cmd:validator-a", Node: "validator-a", Role: model.RoleValidator},
		Cmd:    []string{"ssh", "user@host", "journalctl", "-f", "-u", "gnoland"},
		Runner: func(_ context.Context, cmd []string) (io.ReadCloser, <-chan error, error) {
			gotCmd = append([]string(nil), cmd...)
			waitCh := make(chan error, 1)
			waitCh <- nil
			close(waitCh)
			payload := `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}` + "\n"
			return io.NopCloser(strings.NewReader(payload)), waitCh, nil
		},
	}

	lines, errs := src.Stream(context.Background())
	line := waitForLine(t, lines)

	require.Equal(t, []string{"ssh", "user@host", "journalctl", "-f", "-u", "gnoland"}, gotCmd)
	require.Equal(t, `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`, line.Raw)
	require.Equal(t, "cmd:validator-a", line.Path)
	require.Equal(t, "validator-a", line.Node)
	require.Equal(t, model.RoleValidator, line.Role)

	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

func TestCmdSourceMultipleLinesAndLineNumbers(t *testing.T) {
	src := &CmdSource{
		Source: model.Source{Path: "cmd:val1", Node: "val1", Role: model.RoleValidator},
		Cmd:    []string{"echo", "hello"},
		Runner: func(_ context.Context, _ []string) (io.ReadCloser, <-chan error, error) {
			waitCh := make(chan error, 1)
			waitCh <- nil
			close(waitCh)
			payload := "line one\nline two\nline three\n"
			return io.NopCloser(strings.NewReader(payload)), waitCh, nil
		},
	}

	lines, _ := src.Stream(context.Background())
	first := waitForLine(t, lines)
	second := waitForLine(t, lines)
	third := waitForLine(t, lines)

	require.Equal(t, "line one", first.Raw)
	require.Equal(t, 1, first.LineNo)
	require.Equal(t, "line two", second.Raw)
	require.Equal(t, 2, second.LineNo)
	require.Equal(t, "line three", third.Raw)
	require.Equal(t, 3, third.LineNo)
}

func TestCmdSourceRunnerErrorIsReturned(t *testing.T) {
	src := &CmdSource{
		Source: model.Source{Path: "cmd:missing", Node: "missing"},
		Cmd:    []string{"nonexistent-binary"},
		Runner: func(_ context.Context, _ []string) (io.ReadCloser, <-chan error, error) {
			return nil, nil, errors.New("binary not found")
		},
	}

	lines, errs := src.Stream(context.Background())
	_, ok := <-lines
	require.False(t, ok)

	err := <-errs
	require.ErrorContains(t, err, "binary not found")
}

func TestCmdSourceCommandExitErrorIsReturned(t *testing.T) {
	src := &CmdSource{
		Source: model.Source{Path: "cmd:val", Node: "val"},
		Cmd:    []string{"false"},
		Runner: func(_ context.Context, _ []string) (io.ReadCloser, <-chan error, error) {
			waitCh := make(chan error, 1)
			waitCh <- errors.New("exit status 1")
			close(waitCh)
			return io.NopCloser(strings.NewReader("")), waitCh, nil
		},
	}

	lines, errs := src.Stream(context.Background())
	<-lines

	err := <-errs
	require.ErrorContains(t, err, "exit status 1")
}

func TestCmdSourceContextCancellationStopsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	blocked := make(chan struct{})
	src := &CmdSource{
		Source: model.Source{Path: "cmd:val", Node: "val"},
		Cmd:    []string{"cat"},
		Runner: func(runCtx context.Context, _ []string) (io.ReadCloser, <-chan error, error) {
			pr, pw := io.Pipe()
			waitCh := make(chan error, 1)
			go func() {
				close(blocked)
				<-runCtx.Done()
				_ = pw.Close()
				waitCh <- context.Canceled
				close(waitCh)
			}()
			return pr, waitCh, nil
		},
	}

	lines, errs := src.Stream(ctx)

	// Wait until the runner is blocking, then cancel.
	<-blocked
	cancel()

	// Both channels must close without a non-canceled error.
	for lines != nil || errs != nil {
		select {
		case _, ok := <-lines:
			if !ok {
				lines = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			require.True(t, errors.Is(err, context.Canceled), "unexpected error: %v", err)
		}
	}
}

func TestCmdSourcePathHelpers(t *testing.T) {
	require.Equal(t, "cmd:validator-a", CmdSourcePath("validator-a"))
	require.True(t, IsCmdSourcePath("cmd:validator-a"))
	require.False(t, IsCmdSourcePath("docker:validator-a"))
	require.False(t, IsCmdSourcePath("/tmp/validator.log"))
	require.False(t, IsCmdSourcePath("cmd"))
}

package source

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

// blockUntilCancelled returns a runner that blocks the docker-logs session
// until runCtx is cancelled.  Use it for "subsequent" runner calls where the
// test only needs the source to stop cleanly.
func blockUntilCancelled() func(runCtx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
	return func(runCtx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
		pr, pw := io.Pipe()
		waitCh := make(chan error, 1)
		go func() {
			<-runCtx.Done()
			_ = pw.CloseWithError(context.Canceled)
			waitCh <- context.Canceled
			close(waitCh)
		}()
		return pr, waitCh, nil
	}
}

func TestDockerSourceDefaultUsesTailZeroAndStripsOuterTimestamp(t *testing.T) {
	const payload = "2026-04-16T10:00:00.000000000Z {\"level\":\"info\",\"ts\":1776333600,\"msg\":\"Finalizing commit of block\",\"height\":10}\n"

	var gotArgs []string
	callCount := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &DockerSource{
		Source:     model.Source{Path: "docker:validator-a", Node: "validator-a", Role: model.RoleValidator},
		Container:  "validator-a",
		RetryDelay: 10 * time.Millisecond,
		Runner: func(runCtx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			callCount++
			if callCount == 1 {
				// First call: emit one line then exit cleanly (NopCloser is read immediately).
				gotArgs = append([]string(nil), args...)
				waitCh := make(chan error, 1)
				waitCh <- nil
				close(waitCh)
				return io.NopCloser(strings.NewReader(payload)), waitCh, nil
			}
			return blockUntilCancelled()(runCtx, args)
		},
	}

	lines, errs := src.Stream(ctx)
	line := waitForLine(t, lines)
	// Cancel only after the line is safely received — no race with the select.
	cancel()

	// Drain channels.
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

	require.Equal(t, []string{"--follow", "--timestamps", "--tail", "0", "validator-a"}, gotArgs)
	require.Equal(t, `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`, line.Raw)
	require.Equal(t, "docker:validator-a", line.Path)
}

func TestDockerSourceSinceUsesDockerSinceFlag(t *testing.T) {
	since := time.Date(2026, 4, 16, 12, 13, 14, 0, time.UTC)
	// argsCh is used instead of a shared variable to avoid data races between
	// the Stream goroutine (writer) and the test goroutine (reader).
	argsCh := make(chan []string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &DockerSource{
		Source:     model.Source{Path: "docker:validator-b", Node: "validator-b", Role: model.RoleValidator},
		Container:  "validator-b",
		Since:      since,
		RetryDelay: 10 * time.Millisecond,
		Runner: func(runCtx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			select {
			case argsCh <- append([]string(nil), args...):
			default:
			}
			return blockUntilCancelled()(runCtx, args)
		},
	}

	lines, errs := src.Stream(ctx)
	// Block until the first runner call records args, then cancel.
	gotArgs := <-argsCh
	cancel()

	// Drain channels.
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

	require.Equal(t, []string{"--follow", "--timestamps", "--since", "2026-04-16T12:13:14Z", "validator-b"}, gotArgs)
}

func TestDockerSourceRunnerErrorIsReturned(t *testing.T) {
	src := &DockerSource{
		Source:    model.Source{Path: "docker:missing", Node: "missing"},
		Container: "missing",
		Runner: func(ctx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			return nil, nil, errors.New("docker not found")
		},
	}

	lines, errs := src.Stream(context.Background())
	_, ok := <-lines
	require.False(t, ok)

	err := <-errs
	require.ErrorContains(t, err, "docker not found")
}

// TestDockerSourceReconnectsOnContainerRestart verifies that when the docker
// logs stream ends cleanly (container stopped), DockerSource reconnects and
// continues streaming from where it left off.
func TestDockerSourceReconnectsOnContainerRestart(t *testing.T) {
	const ts1 = "2026-04-16T10:00:00.000000001Z"
	const ts2 = "2026-04-16T10:00:01.000000001Z"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var secondArgs []string
	callCount := 0

	src := &DockerSource{
		Source:     model.Source{Path: "docker:val", Node: "val", Role: model.RoleValidator},
		Container:  "val",
		RetryDelay: 10 * time.Millisecond,
		Runner: func(runCtx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			callCount++
			waitCh := make(chan error, 1)
			switch callCount {
			case 1:
				// First connection: emit one line then exit cleanly.
				payload := ts1 + ` {"level":"info","msg":"first","height":1}` + "\n"
				waitCh <- nil
				close(waitCh)
				return io.NopCloser(strings.NewReader(payload)), waitCh, nil
			case 2:
				// Second connection (after reconnect): verify --since was updated, emit line.
				secondArgs = append([]string(nil), args...)
				payload := ts2 + ` {"level":"info","msg":"second","height":2}` + "\n"
				waitCh <- nil
				close(waitCh)
				return io.NopCloser(strings.NewReader(payload)), waitCh, nil
			default:
				return blockUntilCancelled()(runCtx, args)
			}
		},
	}

	lines, errs := src.Stream(ctx)

	first := waitForLine(t, lines)
	require.Equal(t, `{"level":"info","msg":"first","height":1}`, first.Raw)

	second := waitForLine(t, lines)
	require.Equal(t, `{"level":"info","msg":"second","height":2}`, second.Raw)

	// Cancel after both lines received.
	cancel()

	// Drain remaining channels.
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

	require.GreaterOrEqual(t, callCount, 2)
	require.Contains(t, secondArgs, "--since", "reconnect should use --since flag")
}

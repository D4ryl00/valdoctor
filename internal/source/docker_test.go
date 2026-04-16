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

func TestDockerSourceDefaultUsesTailZeroAndStripsOuterTimestamp(t *testing.T) {
	var gotArgs []string
	src := &DockerSource{
		Source:    model.Source{Path: "docker:validator-a", Node: "validator-a", Role: model.RoleValidator},
		Container: "validator-a",
		Runner: func(ctx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			gotArgs = append([]string(nil), args...)
			waitCh := make(chan error, 1)
			waitCh <- nil
			close(waitCh)
			payload := "2026-04-16T10:00:00.000000000Z {\"level\":\"info\",\"ts\":1776333600,\"msg\":\"Finalizing commit of block\",\"height\":10}\n"
			return io.NopCloser(strings.NewReader(payload)), waitCh, nil
		},
	}

	lines, errs := src.Stream(context.Background())
	line := waitForLine(t, lines)

	require.Equal(t, []string{"--follow", "--timestamps", "--tail", "0", "validator-a"}, gotArgs)
	require.Equal(t, `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`, line.Raw)
	require.Equal(t, "docker:validator-a", line.Path)

	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

func TestDockerSourceSinceUsesDockerSinceFlag(t *testing.T) {
	since := time.Date(2026, 4, 16, 12, 13, 14, 0, time.UTC)
	var gotArgs []string
	src := &DockerSource{
		Source:    model.Source{Path: "docker:validator-b", Node: "validator-b", Role: model.RoleValidator},
		Container: "validator-b",
		Since:     since,
		Runner: func(ctx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
			gotArgs = append([]string(nil), args...)
			waitCh := make(chan error, 1)
			waitCh <- nil
			close(waitCh)
			return io.NopCloser(strings.NewReader("")), waitCh, nil
		},
	}

	lines, errs := src.Stream(context.Background())
	<-lines
	<-errs

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

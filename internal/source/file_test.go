package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestFileSourceFollowsNewLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	require.NoError(t, os.WriteFile(path, []byte("2026-04-16T10:00:00Z INFO old\n"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "validator-a", Role: model.RoleValidator},
		PollInterval: 20 * time.Millisecond,
	}
	lines, errs := src.Stream(ctx)
	time.Sleep(100 * time.Millisecond)

	appendLine(t, path, "2026-04-16T10:00:01Z INFO new\n")

	line := waitForLine(t, lines)
	require.Contains(t, line.Raw, "new")

	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

func TestFileSourceBootstrapReadsExistingLinesThenFollows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	require.NoError(t, os.WriteFile(path, []byte("2026-04-16T10:00:00Z INFO old\n"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "validator-a", Role: model.RoleValidator},
		Bootstrap:    true,
		PollInterval: 20 * time.Millisecond,
	}
	lines, errs := src.Stream(ctx)

	first := waitForLine(t, lines)
	require.Contains(t, first.Raw, "old")

	appendLine(t, path, "2026-04-16T10:00:01Z INFO new\n")

	second := waitForLine(t, lines)
	require.Contains(t, second.Raw, "new")

	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

func TestFileSourceBootstrapDoesNotRequireTimestampSniff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	content := "" +
		"val1-1  | 2026-04-20T17:41:32.373Z\tINFO\tSwitchToConsensus\t{\"module\":\"consensus\"}\n" +
		"val1-1  | 2026-04-20T17:41:32.376Z\tINFO\tTimed out\t{\"module\":\"consensus\",\"height\":1,\"round\":0,\"step\":\"RoundStepNewHeight\"}\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "val1", Role: model.RoleValidator},
		Bootstrap:    true,
		PollInterval: 20 * time.Millisecond,
	}
	lines, _ := src.Stream(ctx)

	first := waitForLine(t, lines)
	require.Contains(t, first.Raw, "SwitchToConsensus")

	second := waitForLine(t, lines)
	require.Contains(t, second.Raw, `"height":1`)
}

func TestFileSourceSinceBootstrapsThenFollows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	content := "" +
		"2026-04-16T10:00:00Z INFO old\n" +
		"2026-04-16T10:00:05Z INFO keep\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "validator-a", Role: model.RoleValidator},
		Since:        time.Date(2026, 4, 16, 10, 0, 3, 0, time.UTC),
		PollInterval: 20 * time.Millisecond,
	}
	lines, _ := src.Stream(ctx)

	first := waitForLine(t, lines)
	require.Contains(t, first.Raw, "keep")

	appendLine(t, path, "2026-04-16T10:00:06Z INFO tail\n")

	second := waitForLine(t, lines)
	require.Contains(t, second.Raw, "tail")
}

func TestFileSourceDetectsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "validator-a", Role: model.RoleValidator},
		PollInterval: 20 * time.Millisecond,
	}
	lines, _ := src.Stream(ctx)
	time.Sleep(100 * time.Millisecond)

	appendLine(t, path, "2026-04-16T10:00:01Z INFO before-rotate\n")
	require.Contains(t, waitForLine(t, lines).Raw, "before-rotate")

	rotated := path + ".1"
	require.NoError(t, os.Rename(path, rotated))
	require.NoError(t, os.WriteFile(path, []byte("2026-04-16T10:00:02Z INFO after-rotate\n"), 0o644))

	require.Contains(t, waitForLine(t, lines).Raw, "after-rotate")
}

func TestFileSourceFallsBackToPollingWhenWatcherSetupFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.log")
	require.NoError(t, os.WriteFile(path, []byte("2026-04-16T10:00:00Z INFO old\n"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{
		Source:       model.Source{Path: path, Node: "validator-a", Role: model.RoleValidator},
		PollInterval: 20 * time.Millisecond,
		NewWatcher: func() (*fsnotify.Watcher, error) {
			return nil, errors.New("watcher unavailable")
		},
	}
	lines, errs := src.Stream(ctx)
	time.Sleep(100 * time.Millisecond)

	appendLine(t, path, "2026-04-16T10:00:01Z INFO from-poll\n")

	line := waitForLine(t, lines)
	require.Contains(t, line.Raw, "from-poll")

	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer file.Close()

	_, err = file.WriteString(line)
	require.NoError(t, err)
}

func waitForLine(t *testing.T, lines <-chan Line) Line {
	t.Helper()

	select {
	case line := <-lines:
		return line
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for line")
		return Line{}
	}
}

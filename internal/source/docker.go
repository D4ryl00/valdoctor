package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type dockerLogsRunner func(ctx context.Context, args []string) (io.ReadCloser, <-chan error, error)

const defaultDockerBootstrapTail = "50000"

type DockerSource struct {
	Source     model.Source
	Container  string
	Since      time.Time
	Runner     dockerLogsRunner
	RetryDelay time.Duration // delay before reconnecting; 0 → 3s default
}

func (d *DockerSource) Name() string {
	return d.Source.Path
}

// Stream ingests docker logs for d.Container and emits Lines until ctx is
// cancelled.  When the container stops (docker logs --follow exits cleanly),
// Stream waits 3 seconds and reconnects automatically so that a container
// restart is transparent to the caller.  The --since flag is advanced to the
// last-seen log timestamp on each reconnect to avoid replaying lines.
func (d *DockerSource) Stream(ctx context.Context) (<-chan Line, <-chan error) {
	lines := make(chan Line)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		since := d.Since
		for {
			lastSeen, err := d.streamOnce(ctx, since, lines)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					errs <- err
				}
				return
			}
			// Advance since so the next connection doesn't replay old lines.
			if !lastSeen.IsZero() {
				since = lastSeen
			}
			// Stream ended cleanly (container stopped or restarted).
			// Wait briefly then reconnect.
			delay := d.RetryDelay
			if delay == 0 {
				delay = 3 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()

	return lines, errs
}

// streamOnce runs one docker-logs session. It returns the timestamp of the
// last line received (used as --since on reconnect) and any fatal error.
// A nil error means the stream ended cleanly; the caller should reconnect.
func (d *DockerSource) streamOnce(ctx context.Context, since time.Time, lines chan<- Line) (lastSeen time.Time, err error) {
	reader, waitCh, startErr := d.runner()(ctx, d.commandArgsWithSince(since))
	if startErr != nil {
		if errors.Is(startErr, context.Canceled) {
			return time.Time{}, context.Canceled
		}
		return time.Time{}, startErr
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		ts, raw := parseDockerLine(scanner.Text())
		if !ts.IsZero() {
			lastSeen = ts
		}
		select {
		case lines <- Line{
			Raw:    raw,
			Path:   d.Source.Path,
			Node:   d.Source.Node,
			Role:   d.Source.Role,
			LineNo: lineNo,
		}:
		case <-ctx.Done():
			<-waitCh
			return lastSeen, context.Canceled
		}
	}

	if scanErr := scanner.Err(); scanErr != nil && !errors.Is(scanErr, context.Canceled) {
		<-waitCh
		return lastSeen, scanErr
	}

	if waitErr := <-waitCh; waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		return lastSeen, waitErr
	}
	return lastSeen, nil
}

func (d *DockerSource) commandArgsWithSince(since time.Time) []string {
	args := []string{"--follow", "--timestamps"}
	switch {
	case !since.IsZero():
		args = append(args, "--since", since.UTC().Format(time.RFC3339Nano))
	case !d.Since.IsZero():
		args = append(args, "--since", d.Since.UTC().Format(time.RFC3339))
	default:
		// Bootstrap a bounded recent backlog so live mode can reconstruct the
		// current chain state even when attaching after a fault has already
		// happened. Without this, halted chains look like "tip 1" forever
		// because docker logs would only stream future lines.
		args = append(args, "--tail", defaultDockerBootstrapTail)
	}
	args = append(args, d.Container)
	return args
}

func (d *DockerSource) runner() dockerLogsRunner {
	if d.Runner != nil {
		return d.Runner
	}
	return execDockerLogs
}

func execDockerLogs(ctx context.Context, args []string) (io.ReadCloser, <-chan error, error) {
	pr, pw := io.Pipe()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"logs"}, args...)...)
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, nil, fmt.Errorf("starting docker logs: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		waitCh <- err
		close(waitCh)
	}()

	return pr, waitCh, nil
}

// parseDockerLine extracts the RFC3339Nano timestamp prepended by docker logs
// --timestamps and returns (timestamp, stripped-line). If the line does not
// carry a Docker timestamp the zero time and the original line are returned.
func parseDockerLine(raw string) (time.Time, string) {
	raw = strings.TrimRight(raw, "\r")
	token, rest, ok := cutDockerToken(raw)
	if !ok {
		return time.Time{}, raw
	}
	ts, err := time.Parse(time.RFC3339Nano, token)
	if err != nil {
		return time.Time{}, raw
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return ts, raw
	}
	return ts, rest
}

func cutDockerToken(input string) (string, string, bool) {
	input = strings.TrimLeft(input, " \t")
	if input == "" {
		return "", "", false
	}
	idx := strings.IndexAny(input, " \t")
	if idx < 0 {
		return input, "", true
	}
	return input[:idx], input[idx+1:], true
}

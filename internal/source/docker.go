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

type DockerSource struct {
	Source    model.Source
	Container string
	Since     time.Time
	Runner    dockerLogsRunner
}

func (d *DockerSource) Name() string {
	return d.Source.Path
}

func (d *DockerSource) Stream(ctx context.Context) (<-chan Line, <-chan error) {
	lines := make(chan Line)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		reader, waitCh, err := d.runner()(ctx, d.commandArgs())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				errs <- err
			}
			return
		}
		defer reader.Close()

		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		lineNo := 0
		for scanner.Scan() {
			lineNo++
			raw := stripDockerTimestamp(scanner.Text())
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
				return
			}
		}

		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
			<-waitCh
			return
		}

		if err := <-waitCh; err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()

	return lines, errs
}

func (d *DockerSource) commandArgs() []string {
	args := []string{"--follow", "--timestamps"}
	if d.Since.IsZero() {
		args = append(args, "--tail", "0")
	} else {
		args = append(args, "--since", d.Since.UTC().Format(time.RFC3339))
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

func stripDockerTimestamp(raw string) string {
	raw = strings.TrimRight(raw, "\r")
	token, rest, ok := cutDockerToken(raw)
	if !ok {
		return raw
	}
	if _, err := time.Parse(time.RFC3339Nano, token); err != nil {
		return raw
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return raw
	}
	return rest
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

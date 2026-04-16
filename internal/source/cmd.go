package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/D4ryl00/valdoctor/internal/model"
)

// cmdRunner is the injection point for starting a command and obtaining its
// combined stdout/stderr stream.  The real implementation uses os/exec; tests
// can substitute a stub that returns a pre-filled reader.
type cmdRunner func(ctx context.Context, cmd []string) (io.ReadCloser, <-chan error, error)

// CmdSource streams log lines from the stdout of an arbitrary command.
// It is the generic counterpart to DockerSource: anything that can be expressed
// as a shell pipeline — SSH tail, journalctl, kubectl logs — works here.
//
// Example commands:
//
//	ssh user@host journalctl -f -u gnoland --output=short-iso
//	ssh -i /home/ops/.ssh/id_ed25519 user@host tail -f /var/log/gnoland/gnoland.log
//	kubectl logs -f -n mainnet pod/validator-0
type CmdSource struct {
	Source model.Source
	// Cmd is the command and its arguments, e.g.
	// []string{"ssh", "user@host", "journalctl", "-f", "-u", "gnoland"}.
	// The first element is the executable; the rest are its arguments.
	// No shell expansion is performed — for complex pipelines wrap in a script.
	Cmd    []string
	Runner cmdRunner // nil → execCmd (real os/exec)
}

func (c *CmdSource) Name() string {
	return c.Source.Path
}

func (c *CmdSource) Stream(ctx context.Context) (<-chan Line, <-chan error) {
	lines := make(chan Line)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		reader, waitCh, err := c.runner()(ctx, c.Cmd)
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
			raw := scanner.Text()
			select {
			case lines <- Line{
				Raw:    raw,
				Path:   c.Source.Path,
				Node:   c.Source.Node,
				Role:   c.Source.Role,
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

func (c *CmdSource) runner() cmdRunner {
	if c.Runner != nil {
		return c.Runner
	}
	return execCmd
}

func execCmd(ctx context.Context, cmd []string) (io.ReadCloser, <-chan error, error) {
	if len(cmd) == 0 {
		return nil, nil, fmt.Errorf("empty command")
	}
	pr, pw := io.Pipe()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, nil, fmt.Errorf("starting command %q: %w", cmd[0], err)
	}

	waitCh := make(chan error, 1)
	go func() {
		err := c.Wait()
		_ = pw.Close()
		waitCh <- err
		close(waitCh)
	}()

	return pr, waitCh, nil
}

// CmdSourcePath returns the canonical source path for a CmdSource node.
// It mirrors dockerSourcePath so the rest of the pipeline can tell source types apart.
func CmdSourcePath(nodeName string) string {
	return "cmd:" + nodeName
}

// IsCmdSourcePath reports whether path was produced by CmdSourcePath.
func IsCmdSourcePath(path string) bool {
	return len(path) > 4 && path[:4] == "cmd:"
}

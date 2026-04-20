package source

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/fsnotify/fsnotify"
)

type FileSource struct {
	Source       model.Source
	Since        time.Time
	Bootstrap    bool
	PollInterval time.Duration
	NewWatcher   func() (*fsnotify.Watcher, error)
}

func (f *FileSource) Name() string {
	return f.Source.Path
}

func (f *FileSource) Stream(ctx context.Context) (<-chan Line, <-chan error) {
	lines := make(chan Line)
	errs := make(chan error, 1)

	go func() {
		defer close(lines)
		defer close(errs)

		if strings.HasSuffix(strings.ToLower(f.Source.Path), ".gz") {
			if err := f.streamGzip(ctx, lines); err != nil && !errors.Is(err, context.Canceled) {
				errs <- err
			}
			return
		}

		if err := f.followFile(ctx, lines); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()

	return lines, errs
}

func (f *FileSource) streamGzip(ctx context.Context, out chan<- Line) error {
	file, err := os.Open(f.Source.Path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()

	return f.scanReader(ctx, reader, out, !f.Since.IsZero())
}

func (f *FileSource) followFile(ctx context.Context, out chan<- Line) error {
	poll := f.PollInterval
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}

	file, err := os.Open(f.Source.Path)
	if err != nil {
		return err
	}

	startAtBeginning := f.Bootstrap || !f.Since.IsZero()
	if !startAtBeginning {
		lineNo, err := countLines(file)
		if err != nil {
			return err
		}
		offset, err := file.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		state := fileState{file: file, offset: offset, lineNo: lineNo}
		return f.followOpenFile(ctx, out, &state, false, poll)
	}

	state := fileState{file: file}
	return f.followOpenFile(ctx, out, &state, !f.Since.IsZero(), poll)
}

type fileState struct {
	file       *os.File
	offset     int64
	lineNo     int
	remainder  string
	bootstraps bool
}

func (f *FileSource) followOpenFile(ctx context.Context, out chan<- Line, state *fileState, applySince bool, poll time.Duration) error {
	state.bootstraps = applySince
	watcher, err := f.openWatcher()
	if err == nil {
		defer watcher.Close()
	}
	defer func() {
		if state.file != nil {
			_ = state.file.Close()
		}
	}()

	for {
		readSomething, err := f.readAvailable(ctx, state, out, state.bootstraps)
		if err != nil {
			return err
		}
		if state.bootstraps && !readSomething {
			state.bootstraps = false
		}

		if err := f.waitForChange(ctx, watcher, poll); err != nil {
			return err
		}

		latestInfo, err := os.Stat(f.Source.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		currentInfo, err := state.file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(currentInfo, latestInfo) || latestInfo.Size() < state.offset {
			if err := state.file.Close(); err != nil {
				return err
			}
			reopened, err := os.Open(f.Source.Path)
			if err != nil {
				return err
			}
			state.file = reopened
			state.offset = 0
			state.remainder = ""
		}
	}
}

func (f *FileSource) openWatcher() (*fsnotify.Watcher, error) {
	newWatcher := f.NewWatcher
	if newWatcher == nil {
		newWatcher = fsnotify.NewWatcher
	}

	watcher, err := newWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(filepath.Dir(f.Source.Path)); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	return watcher, nil
}

func (f *FileSource) waitForChange(ctx context.Context, watcher *fsnotify.Watcher, poll time.Duration) error {
	timer := time.NewTimer(poll)
	defer timer.Stop()

	for {
		if watcher == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				watcher = nil
				continue
			}
			if f.isRelevantEvent(event) {
				return nil
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				watcher = nil
				continue
			}
			watcher = nil
		}
	}
}

func (f *FileSource) isRelevantEvent(event fsnotify.Event) bool {
	if filepath.Clean(event.Name) != filepath.Clean(f.Source.Path) {
		return false
	}
	return event.Has(fsnotify.Write) ||
		event.Has(fsnotify.Create) ||
		event.Has(fsnotify.Rename) ||
		event.Has(fsnotify.Remove) ||
		event.Has(fsnotify.Chmod)
}

func (f *FileSource) readAvailable(ctx context.Context, state *fileState, out chan<- Line, applySince bool) (bool, error) {
	if _, err := state.file.Seek(state.offset, io.SeekStart); err != nil {
		return false, err
	}

	var readSomething bool
	buf := make([]byte, 32*1024)

	for {
		n, err := state.file.Read(buf)
		if n > 0 {
			readSomething = true
			state.offset += int64(n)
			state.remainder += string(buf[:n])
			for {
				newline := strings.IndexByte(state.remainder, '\n')
				if newline < 0 {
					break
				}
				line := strings.TrimRight(state.remainder[:newline], "\r")
				state.remainder = state.remainder[newline+1:]
				state.lineNo++
				if err := f.emitLine(ctx, out, line, state.lineNo, applySince); err != nil {
					return readSomething, err
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return readSomething, nil
		}
		return readSomething, err
	}
}

func (f *FileSource) scanReader(ctx context.Context, reader io.Reader, out chan<- Line, applySince bool) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if err := f.emitLine(ctx, out, scanner.Text(), lineNo, applySince); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (f *FileSource) emitLine(ctx context.Context, out chan<- Line, raw string, lineNo int, applySince bool) error {
	if applySince {
		ts, ok := sniffTimestamp(raw)
		if !ok || ts.Before(f.Since) {
			return nil
		}
	}

	select {
	case out <- Line{
		Raw:    raw,
		Path:   f.Source.Path,
		Node:   f.Source.Node,
		Role:   f.Source.Role,
		LineNo: lineNo,
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func countLines(file *os.File) (int, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func sniffTimestamp(raw string) (time.Time, bool) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return time.Time{}, false
	}

	if strings.HasPrefix(clean, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(clean), &payload); err == nil {
			if ts, ok := payload["ts"].(float64); ok {
				sec := int64(ts)
				nsec := int64((ts - float64(sec)) * float64(time.Second))
				return time.Unix(sec, nsec).UTC(), true
			}
			if ts, ok := payload["time"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					return parsed.UTC(), true
				}
			}
			if nested, ok := payload["log"].(string); ok {
				return sniffTimestamp(nested)
			}
		}
	}

	fields := strings.Fields(clean)
	if len(fields) == 0 {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, fields[0])
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

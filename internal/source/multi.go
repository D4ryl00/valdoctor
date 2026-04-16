package source

import (
	"context"
	"sync"
	"sync/atomic"
)

type MultiSource struct {
	sources []LogSource
}

func NewMultiSource(sources ...LogSource) *MultiSource {
	return &MultiSource{sources: sources}
}

func (m *MultiSource) Name() string {
	return "multi"
}

func (m *MultiSource) Stream(ctx context.Context) (<-chan Line, <-chan error) {
	out := make(chan Line)
	errs := make(chan error, len(m.sources))

	if len(m.sources) == 0 {
		close(out)
		close(errs)
		return out, errs
	}

	var seq uint64
	var wg sync.WaitGroup

	for _, src := range m.sources {
		lines, childErrs := src.Stream(ctx)
		wg.Add(1)
		go func(lines <-chan Line, childErrs <-chan error) {
			defer wg.Done()

			for lines != nil || childErrs != nil {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-lines:
					if !ok {
						lines = nil
						continue
					}
					line.SeqNo = atomic.AddUint64(&seq, 1)
					select {
					case out <- line:
					case <-ctx.Done():
						return
					}
				case err, ok := <-childErrs:
					if !ok {
						childErrs = nil
						continue
					}
					select {
					case errs <- err:
					case <-ctx.Done():
						return
					}
				}
			}
		}(lines, childErrs)
	}

	go func() {
		wg.Wait()
		close(out)
		close(errs)
	}()

	return out, errs
}

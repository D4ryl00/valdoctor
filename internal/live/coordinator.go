package live

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/D4ryl00/valdoctor/internal/analyze"
	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/parse"
	"github.com/D4ryl00/valdoctor/internal/source"
	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

type Coordinator struct {
	Source           source.LogSource
	Store            storepkg.Store
	Genesis          model.Genesis
	Metadata         model.Metadata
	Sources          []model.Source
	ClosureEvaluator ClosureEvaluator
	MaxHistory       int
	Debounce         time.Duration
	OnTipAdvanced    func(int64)
	OnHeightClosed   func(int64)
	OnIncidentUpdate func(model.IncidentCard)

	mu               sync.Mutex
	rebuildGen       map[int64]int
	closedAt         map[int64]time.Time
	finalizeObserved map[int64]map[string]time.Time
	resolver         *IdentityResolver
	incidents        IncidentEngine
}

func (c *Coordinator) Run(ctx context.Context) error {
	if c.MaxHistory <= 0 {
		c.MaxHistory = 500
	}
	if c.Debounce <= 0 {
		c.Debounce = 50 * time.Millisecond
	}
	if c.rebuildGen == nil {
		c.rebuildGen = map[int64]int{}
	}
	if c.closedAt == nil {
		c.closedAt = map[int64]time.Time{}
	}
	if c.finalizeObserved == nil {
		c.finalizeObserved = map[int64]map[string]time.Time{}
	}
	if c.resolver == nil {
		c.resolver = &IdentityResolver{
			Genesis:  c.Genesis,
			Metadata: c.Metadata,
			Sources:  c.Sources,
		}
	}

	lines, errs := c.Source.Stream(ctx)

	for lines != nil || errs != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			c.handleLine(line)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			return err
		}
	}

	c.flushRebuilds()
	return nil
}

func (c *Coordinator) handleLine(line source.Line) {
	src := model.Source{
		Path: line.Path,
		Node: line.Node,
		Role: line.Role,
	}

	event, _ := parse.ParseLogLine(src, line.Raw, line.LineNo)
	if event.Kind == model.EventUnknown || event.Kind == model.EventKnownNoise {
		return
	}

	_ = c.Store.AppendEvent(event)

	if event.Height > 0 {
		prevTip := c.Store.CurrentTip()
		if event.Height > prevTip {
			c.Store.SetTip(event.Height)
			if c.OnTipAdvanced != nil {
				c.OnTipAdvanced(event.Height)
			}
			c.pruneState(event.Height)
		}
		c.scheduleRebuild(event.Height)
	}

	if event.Kind == model.EventFinalizeCommit {
		c.observeFinalizeCommit(event)
	}
}

func (c *Coordinator) observeFinalizeCommit(event model.Event) {
	validatorNames := c.validatorSourceSet()
	if _, ok := validatorNames[event.Node]; !ok {
		return
	}

	commitAt := event.Timestamp
	if commitAt.IsZero() {
		commitAt = time.Now().UTC()
	}

	c.mu.Lock()
	observed := c.finalizeObserved[event.Height]
	if observed == nil {
		observed = map[string]time.Time{}
		c.finalizeObserved[event.Height] = observed
	}
	observed[event.Node] = commitAt
	if _, alreadyClosed := c.closedAt[event.Height]; alreadyClosed {
		c.mu.Unlock()
		return
	}
	if !c.ClosureEvaluator.ShouldClose(observed) {
		c.mu.Unlock()
		return
	}

	c.closedAt[event.Height] = commitAt
	c.mu.Unlock()

	if c.OnHeightClosed != nil {
		c.OnHeightClosed(event.Height)
	}
	c.scheduleRebuild(event.Height)
	c.scheduleGraceRebuild(event.Height)
}

func (c *Coordinator) scheduleRebuild(height int64) {
	c.mu.Lock()
	c.rebuildGen[height]++
	gen := c.rebuildGen[height]
	c.mu.Unlock()

	time.AfterFunc(c.Debounce, func() {
		c.mu.Lock()
		current := c.rebuildGen[height]
		if gen != current {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		c.rebuildHeight(height)
	})
}

func (c *Coordinator) scheduleGraceRebuild(height int64) {
	if c.ClosureEvaluator.GraceWindow <= 0 {
		return
	}
	time.AfterFunc(c.ClosureEvaluator.GraceWindow, func() {
		c.rebuildHeight(height)
	})
}

func (c *Coordinator) rebuildHeight(height int64) {
	events := c.Store.EventsForHeight(height)
	if len(events) == 0 {
		return
	}

	sortEvents(events)

	report := analyze.BuildHeightReport(analyze.HeightInput{
		Height:   height,
		Genesis:  c.Genesis,
		Sources:  c.Sources,
		Metadata: c.Metadata,
		Events:   events,
	})

	status := model.HeightActive
	c.mu.Lock()
	closedAt, ok := c.closedAt[height]
	c.mu.Unlock()
	if ok {
		status = model.HeightClosed
	}

	gracePassed := c.ClosureEvaluator.GracePassed(closedAt, time.Now().UTC())
	propagation := BuildPropagation(events, height, c.resolver, c.ClosureEvaluator.ValidatorSources, gracePassed)

	_ = c.Store.SetHeightEntry(model.HeightEntry{
		Height:      height,
		Status:      status,
		Report:      report,
		Propagation: propagation,
		LastUpdated: time.Now().UTC(),
	})

	c.refreshNodeStates()
	c.refreshIncidents()
}

func (c *Coordinator) refreshNodeStates() {
	tip := c.Store.CurrentTip()
	if tip <= 0 {
		return
	}

	start := tip - int64(c.MaxHistory)
	if start < 1 {
		start = 1
	}

	events := make([]model.Event, 0)
	for height := start; height <= tip; height++ {
		events = append(events, c.Store.EventsForHeight(height)...)
	}
	sortEvents(events)

	summaries := analyze.BuildNodeSummaries(c.Sources, events, nil)

	// In live mode a validator that stops logging will have StallDuration ≈ 0
	// because BuildNodeSummaries measures stall against the node's own last event
	// timestamp rather than the current time. Override StallDuration for any
	// validator that is behind the chain tip so stall incidents fire correctly.
	now := time.Now().UTC()
	for i, summary := range summaries {
		if summary.Role == model.RoleValidator && summary.HighestCommit < tip && !summary.LastCommitTime.IsZero() {
			if stall := now.Sub(summary.LastCommitTime); stall > summary.StallDuration {
				summaries[i].StallDuration = stall
			}
		}
	}

	states := make([]model.NodeState, 0, len(summaries))
	for _, summary := range summaries {
		states = append(states, model.NodeState{
			Summary:   summary,
			UpdatedAt: now,
		})
	}
	c.Store.SetNodeStates(states)
}

func (c *Coordinator) pruneState(tip int64) {
	if c.MaxHistory <= 0 {
		return
	}
	minHeight := tip - int64(c.MaxHistory)
	if minHeight <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for height := range c.rebuildGen {
		if height < minHeight {
			delete(c.rebuildGen, height)
		}
	}
	for height := range c.closedAt {
		if height < minHeight {
			delete(c.closedAt, height)
		}
	}
	for height := range c.finalizeObserved {
		if height < minHeight {
			delete(c.finalizeObserved, height)
		}
	}
}

func (c *Coordinator) flushRebuilds() {
	c.mu.Lock()
	heights := make([]int64, 0, len(c.rebuildGen))
	for height := range c.rebuildGen {
		heights = append(heights, height)
	}
	c.mu.Unlock()

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	for _, height := range heights {
		c.rebuildHeight(height)
	}
}

func (c *Coordinator) refreshIncidents() {
	now := time.Now().UTC()
	updates := c.incidents.Reconcile(
		c.Store,
		c.Store.CurrentTip(),
		c.collectBufferedEvents(),
		c.Store.RecentHeights(0),
		c.Store.NodeStates(),
		now,
	)

	if c.OnIncidentUpdate != nil {
		for _, card := range updates {
			c.OnIncidentUpdate(card)
		}
	}
}

func (c *Coordinator) collectBufferedEvents() []model.Event {
	tip := c.Store.CurrentTip()
	if tip <= 0 {
		return nil
	}

	start := tip - int64(c.MaxHistory)
	if start < 1 {
		start = 1
	}

	events := make([]model.Event, 0)
	for height := start; height <= tip; height++ {
		events = append(events, c.Store.EventsForHeight(height)...)
	}
	sortEvents(events)
	return events
}

func (c *Coordinator) validatorSourceSet() map[string]struct{} {
	set := make(map[string]struct{}, len(c.ClosureEvaluator.ValidatorSources))
	for _, name := range c.ClosureEvaluator.ValidatorSources {
		set[name] = struct{}{}
	}
	return set
}

func sortEvents(events []model.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].HasTimestamp && events[j].HasTimestamp {
			if !events[i].Timestamp.Equal(events[j].Timestamp) {
				return events[i].Timestamp.Before(events[j].Timestamp)
			}
		} else if events[i].HasTimestamp != events[j].HasTimestamp {
			return events[i].HasTimestamp
		}
		if events[i].Path != events[j].Path {
			return events[i].Path < events[j].Path
		}
		return events[i].Line < events[j].Line
	})
}

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
	rebuildMu        sync.Mutex // serializes rebuildHeight; prevents races in IncidentEngine.cards
	rebuildGen       map[int64]int
	closedAt         map[int64]time.Time
	finalizeObserved map[int64]map[string]time.Time
	lineSeenAt       map[string]time.Time
	peerStatsByNode  map[string]parse.ParseStats
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
	if c.lineSeenAt == nil {
		c.lineSeenAt = map[string]time.Time{}
	}
	if c.peerStatsByNode == nil {
		c.peerStatsByNode = map[string]parse.ParseStats{}
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
	c.refreshNodeStatesWithMode(true)
	c.refreshIncidents()
	return nil
}

func (c *Coordinator) handleLine(line source.Line) {
	c.mu.Lock()
	c.lineSeenAt[line.Node] = time.Now().UTC()
	c.mu.Unlock()

	src := model.Source{
		Path: line.Path,
		Node: line.Node,
		Role: line.Role,
	}

	event, _ := parse.ParseLogLine(src, line.Raw, line.LineNo)
	c.observePeerGossip(event)
	if event.Kind == model.EventUnknown || event.Kind == model.EventKnownNoise {
		return
	}

	// Events like peer-add/drop and remote-signer events carry no block height.
	// The store discards events with Height == 0, so we attach them to the
	// current tip (or height 1 before any commit is seen) so that
	// BuildNodeSummaries can count them when refreshing node states.
	if event.Height <= 0 {
		tip := c.Store.CurrentTip()
		if tip <= 0 {
			tip = 1
		}
		event.Height = tip
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

func (c *Coordinator) observePeerGossip(event model.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := c.peerStatsByNode[event.Node]
	if stats.PeerRoundStates == nil {
		stats.PeerRoundStates = map[string]model.PeerRoundState{}
	}
	parse.CollectPeerGossip(event, &stats)
	c.peerStatsByNode[event.Node] = stats
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
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()

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
	c.refreshNodeStatesWithMode(false)
}

func (c *Coordinator) refreshNodeStatesWithMode(forceWallClock bool) {
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

	summaries := analyze.BuildNodeSummaries(c.Sources, events, c.snapshotPeerStats())

	// Enrich summaries with identity info from the resolver: full validator
	// address (for display) and genesis index (for consistent ordering).
	for i := range summaries {
		identity, ok := c.resolver.ResolveByNode(summaries[i].Name)
		if !ok {
			summaries[i].GenesisIndex = -1
			continue
		}
		if summaries[i].ShortAddr == "" {
			if identity.FullAddr != "" {
				summaries[i].ShortAddr = identity.FullAddr
			} else {
				summaries[i].ShortAddr = identity.ShortAddr
			}
		}
		summaries[i].GenesisIndex = identity.GenesisIndex
	}

	now := time.Now().UTC()
	observedHorizon := time.Time{}
	catchUpThreshold := 30 * time.Second
	for _, summary := range summaries {
		if summary.End.After(observedHorizon) {
			observedHorizon = summary.End
		}
		if summary.AvgBlockTime > 0 {
			if horizon := summary.AvgBlockTime * 5; horizon > catchUpThreshold {
				catchUpThreshold = horizon
			}
		}
	}

	// In live mode, BuildNodeSummaries measures StallDuration as
	// (End - LastCommitTime) where End is the node's most recent log timestamp.
	// For a stopped node, End ≈ LastCommitTime → StallDuration ≈ 0, so stall
	// incidents never fire. Once the stream has caught up near real time, extend
	// the stall window to wall clock so genuinely silent validators are reported.
	// While historical backlogs are still replaying, suppress this live-mode
	// extension entirely: per-source replay skew can make healthy validators look
	// dozens of heights behind even though the issue is just ingestion ordering.
	replayingHistory := !forceWallClock && !observedHorizon.IsZero() && now.Sub(observedHorizon) > catchUpThreshold
	if replayingHistory {
		states := make([]model.NodeState, 0, len(summaries))
		for _, summary := range summaries {
			states = append(states, model.NodeState{
				Summary:   summary,
				UpdatedAt: now,
			})
		}
		c.Store.SetNodeStates(states)
		return
	}

	for i, summary := range summaries {
		if summary.Role != model.RoleValidator {
			continue
		}
		if summary.HighestCommit >= tip || summary.LastCommitTime.IsZero() {
			continue
		}
		// Determine "silence" grace: one avg block time, min 2 s.
		grace := summary.AvgBlockTime
		if grace < 2*time.Second {
			grace = 2 * time.Second
		}
		ingestGrace := grace
		if ingestGrace > 5*time.Second {
			ingestGrace = 5 * time.Second
		}
		if !forceWallClock && c.nodeRecentlySeen(summary.Name, now, ingestGrace) {
			continue
		}
		// If the node's last log event is older than the grace window, the node
		// has stopped emitting logs → extend StallDuration out to wall clock.
		if summary.End.IsZero() || now.Sub(summary.End) >= grace {
			if stall := now.Sub(summary.LastCommitTime); stall > summaries[i].StallDuration {
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

func (c *Coordinator) nodeRecentlySeen(node string, now time.Time, grace time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	seenAt := c.lineSeenAt[node]
	if seenAt.IsZero() {
		return false
	}
	return now.Sub(seenAt) < grace
}

func (c *Coordinator) snapshotPeerStats() map[string]analyze.NodePeerStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.peerStatsByNode) == 0 {
		return nil
	}

	out := make(map[string]analyze.NodePeerStats, len(c.peerStatsByNode))
	for node, ps := range c.peerStatsByNode {
		roundStates := make(map[string]model.PeerRoundState, len(ps.PeerRoundStates))
		for peer, state := range ps.PeerRoundStates {
			roundStates[peer] = state
		}
		out[node] = analyze.NodePeerStats{
			MaxVoteHeight: ps.MaxPeerVoteHeight,
			RoundStates:   roundStates,
			StuckHeight:   ps.StuckHeight,
		}
	}
	return out
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

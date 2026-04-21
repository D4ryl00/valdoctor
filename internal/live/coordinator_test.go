package live

import (
	"context"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/source"
	"github.com/D4ryl00/valdoctor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorBuildsHeightsAndPrintsProgress(t *testing.T) {
	src := &stubSource{
		lines: []source.Line{
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":11}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
		},
	}

	memStore := store.NewMemoryStore(5)
	var tips []int64
	var closed []int64

	coord := &Coordinator{
		Source:   src,
		Store:    memStore,
		Genesis:  model.Genesis{ChainID: "test-chain"},
		Sources:  []model.Source{{Path: "/tmp/validator-a.log", Node: "validator-a", Role: model.RoleValidator}},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"validator-a"},
		},
		MaxHistory: 5,
		OnTipAdvanced: func(height int64) {
			tips = append(tips, height)
		},
		OnHeightClosed: func(height int64) {
			closed = append(closed, height)
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		_, ok10 := memStore.GetHeight(10)
		_, ok11 := memStore.GetHeight(11)
		return ok10 && ok11
	}, time.Second, 10*time.Millisecond)

	entry, ok := memStore.GetHeight(11)
	require.True(t, ok)
	require.Equal(t, model.HeightClosed, entry.Status)
	require.EqualValues(t, 11, memStore.CurrentTip())
	require.Equal(t, []int64{10, 11}, tips)
	require.Equal(t, []int64{10, 11}, closed)

	nodes := memStore.NodeStates()
	require.Len(t, nodes, 1)
	require.Equal(t, "validator-a", nodes[0].Summary.Name)
	require.EqualValues(t, 11, nodes[0].Summary.HighestCommit)
}

func TestCoordinatorEmitsPropagationIncidentAfterGrace(t *testing.T) {
	// validator-a signs a prevote at h10 (origin vote).
	// validator-b receives validator-a's precommit (establishing that validator-b
	// IS logging vote receipts at debug level), but never receives validator-a's
	// prevote → genuine miss.
	src := &stubSource{
		lines: []source.Line{
			// validator-a: signed prevote
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Signed and pushed vote","height":10,"round":0,"type":1,"timestamp":"2026-04-15 10:00:00 +0000 UTC","validator address":"AAAAAAAAAAAA0000000000000000000000000000"}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			// validator-a: commit
			{
				Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":10}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			// validator-b: received validator-a's PRECOMMIT (establishes validator-b
			// logs vote receipts at debug level, so missing prevote receipt is real).
			{
				Raw:    `{"level":"debug","ts":1776333601,"msg":"Added to precommit","vote height":10,"vote round":0,"vote":"Vote{0:AAAAAAAAAAAA 10/0/2(Precommit) DEADBEEF0000}"}`,
				Path:   "/tmp/validator-b.log",
				Node:   "validator-b",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
		},
	}

	memStore := store.NewMemoryStore(5)
	updates := make(chan model.IncidentCard, 8)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{
			ChainID: "test-chain",
			Validators: []model.Validator{
				{Name: "validator-a", Address: "AAAAAAAAAAAA0000000000000000000000000000"},
				{Name: "validator-b", Address: "BBBBBBBBBBBB0000000000000000000000000000"},
			},
		},
		Sources: []model.Source{
			{Path: "/tmp/validator-a.log", Node: "validator-a", Role: model.RoleValidator},
			{Path: "/tmp/validator-b.log", Node: "validator-b", Role: model.RoleValidator},
		},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"validator-a", "validator-b"},
			GraceWindow:      20 * time.Millisecond,
		},
		MaxHistory: 5,
		OnIncidentUpdate: func(card model.IncidentCard) {
			updates <- card
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "vote-propagation-miss" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)

	select {
	case update := <-updates:
		require.Equal(t, "vote-propagation-miss", update.Kind)
		require.Equal(t, "active", update.Status)
		require.Equal(t, "vote-propagation-miss-validator-a-validator-b-prevote", update.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incident update")
	}
}

func TestCoordinatorBackfillsPeerAndSignerStateFromLiveTraffic(t *testing.T) {
	src := &stubSource{
		lines: []source.Line{
			{
				Raw:    "2026-04-15T09:59:59Z\tDEBUG\tReceive\t{\"module\":\"consensus\",\"src\":\"Peer{MConn{172.20.0.8:26656} g10s2uwxx2mhut02v3efvvha5hqpf87tnagnkpx4 out}\",\"chId\":32,\"msg\":\"[NewRoundStep H:10 R:0 S:RoundStepPrecommit LCR:0]\"}",
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			{
				Raw:    `{"level":"debug","ts":1776333601,"msg":"Sign request succeeded","module":"remote_signer_client"}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 3,
			},
		},
	}

	memStore := store.NewMemoryStore(5)
	coord := &Coordinator{
		Source:   src,
		Store:    memStore,
		Genesis:  model.Genesis{ChainID: "test-chain"},
		Sources:  []model.Source{{Path: "/tmp/validator-a.log", Node: "validator-a", Role: model.RoleValidator}},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"validator-a"},
		},
		MaxHistory: 5,
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		nodes := memStore.NodeStates()
		return len(nodes) == 1 && nodes[0].Summary.HighestCommit == 10
	}, time.Second, 10*time.Millisecond)

	nodes := memStore.NodeStates()
	require.Len(t, nodes, 1)
	require.Equal(t, 1, nodes[0].Summary.CurrentPeers)
	require.Equal(t, 1, nodes[0].Summary.MaxPeers)
	require.Len(t, nodes[0].Summary.PeerStates, 1)
	require.Equal(t, int64(10), nodes[0].Summary.PeerStates[0].Height)
	require.Equal(t, 1, nodes[0].Summary.SignerConnectCount)
}

func TestCoordinatorDoesNotFlagHealthyHistoricalCatchupAsStalled(t *testing.T) {
	src := &stubSource{
		lines: []source.Line{
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":1}`,
				Path:   "/tmp/val2.log",
				Node:   "val2",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":1}`,
				Path:   "/tmp/val3.log",
				Node:   "val3",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333602,"msg":"Finalizing commit of block","height":1}`,
				Path:   "/tmp/val4.log",
				Node:   "val4",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333603,"msg":"Finalizing commit of block","height":2}`,
				Path:   "/tmp/val2.log",
				Node:   "val2",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			{
				Raw:    `{"level":"info","ts":1776333604,"msg":"Finalizing commit of block","height":2}`,
				Path:   "/tmp/val3.log",
				Node:   "val3",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			{
				Raw:    `{"level":"info","ts":1776333605,"msg":"Finalizing commit of block","height":2}`,
				Path:   "/tmp/val4.log",
				Node:   "val4",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			{
				Raw:    `{"level":"info","ts":1776333606,"msg":"Finalizing commit of block","height":1}`,
				Path:   "/tmp/val1.log",
				Node:   "val1",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333607,"msg":"Finalizing commit of block","height":3}`,
				Path:   "/tmp/val2.log",
				Node:   "val2",
				Role:   model.RoleValidator,
				LineNo: 3,
			},
			{
				Raw:    `{"level":"info","ts":1776333608,"msg":"Finalizing commit of block","height":3}`,
				Path:   "/tmp/val3.log",
				Node:   "val3",
				Role:   model.RoleValidator,
				LineNo: 3,
			},
			{
				Raw:    `{"level":"info","ts":1776333609,"msg":"Finalizing commit of block","height":3}`,
				Path:   "/tmp/val4.log",
				Node:   "val4",
				Role:   model.RoleValidator,
				LineNo: 3,
			},
			{
				Raw:    `{"level":"info","ts":1776333610,"msg":"Finalizing commit of block","height":2}`,
				Path:   "/tmp/val1.log",
				Node:   "val1",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
			{
				Raw:    `{"level":"info","ts":1776333611,"msg":"Finalizing commit of block","height":3}`,
				Path:   "/tmp/val1.log",
				Node:   "val1",
				Role:   model.RoleValidator,
				LineNo: 3,
			},
		},
	}

	memStore := store.NewMemoryStore(16)
	updates := make(chan model.IncidentCard, 16)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: []model.Source{
			{Path: "/tmp/val1.log", Node: "val1", Role: model.RoleValidator},
			{Path: "/tmp/val2.log", Node: "val2", Role: model.RoleValidator},
			{Path: "/tmp/val3.log", Node: "val3", Role: model.RoleValidator},
			{Path: "/tmp/val4.log", Node: "val4", Role: model.RoleValidator},
		},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicyObservedValidatorMajority,
			ValidatorSources: []string{"val1", "val2", "val3", "val4"},
		},
		MaxHistory: 16,
		OnIncidentUpdate: func(card model.IncidentCard) {
			updates <- card
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		return memStore.CurrentTip() == 3
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		entry, ok := memStore.GetHeight(3)
		return ok && entry.Status == model.HeightClosed
	}, time.Second, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	for {
		select {
		case update := <-updates:
			require.NotEqual(t, "stalled-validator", update.Kind)
		default:
			return
		}
	}
}

func TestCoordinatorDoesNotFlagNodeAsStalledWhileBacklogStillStreaming(t *testing.T) {
	src := &delayedStubSource{
		items: []delayedLine{
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":1}`,
					Path:   "/tmp/val1.log",
					Node:   "val1",
					Role:   model.RoleValidator,
					LineNo: 1,
				},
			},
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":1}`,
					Path:   "/tmp/val2.log",
					Node:   "val2",
					Role:   model.RoleValidator,
					LineNo: 1,
				},
				delay: 20 * time.Millisecond,
			},
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333602,"msg":"Finalizing commit of block","height":2}`,
					Path:   "/tmp/val1.log",
					Node:   "val1",
					Role:   model.RoleValidator,
					LineNo: 2,
				},
				delay: 20 * time.Millisecond,
			},
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333603,"msg":"Finalizing commit of block","height":2}`,
					Path:   "/tmp/val2.log",
					Node:   "val2",
					Role:   model.RoleValidator,
					LineNo: 2,
				},
				delay: 20 * time.Millisecond,
			},
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333604,"msg":"Finalizing commit of block","height":3}`,
					Path:   "/tmp/val1.log",
					Node:   "val1",
					Role:   model.RoleValidator,
					LineNo: 3,
				},
				delay: 20 * time.Millisecond,
			},
			{
				line: source.Line{
					Raw:    `{"level":"info","ts":1776333605,"msg":"Finalizing commit of block","height":3}`,
					Path:   "/tmp/val2.log",
					Node:   "val2",
					Role:   model.RoleValidator,
					LineNo: 3,
				},
				delay: 20 * time.Millisecond,
			},
		},
	}

	memStore := store.NewMemoryStore(16)
	updates := make(chan model.IncidentCard, 16)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: []model.Source{
			{Path: "/tmp/val1.log", Node: "val1", Role: model.RoleValidator},
			{Path: "/tmp/val2.log", Node: "val2", Role: model.RoleValidator},
		},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicyObservedValidatorMajority,
			ValidatorSources: []string{"val1", "val2"},
		},
		MaxHistory: 16,
		OnIncidentUpdate: func(card model.IncidentCard) {
			updates <- card
		},
	}

	require.NoError(t, coord.Run(context.Background()))
	require.Eventually(t, func() bool {
		return memStore.CurrentTip() == 3
	}, time.Second, 10*time.Millisecond)

	for {
		select {
		case update := <-updates:
			require.NotEqual(t, "stalled-validator", update.Kind)
		default:
			return
		}
	}
}

func TestCoordinatorPeriodicallyFlagsHistoricalHaltAfterBacklogGoesQuiet(t *testing.T) {
	memStore := store.NewMemoryStore(16)
	now := time.Now().UTC().Add(-20 * time.Second)

	appendCommit := func(node string, ts time.Time, height int64, line int) {
		require.NoError(t, memStore.AppendEvent(model.Event{
			Timestamp:    ts,
			HasTimestamp: true,
			Node:         node,
			Role:         model.RoleValidator,
			Path:         "/tmp/" + node + ".log",
			Line:         line,
			Message:      "Finalizing commit of block",
			Kind:         model.EventFinalizeCommit,
			Height:       height,
		}))
	}

	appendCommit("val1", now, 70, 1)
	appendCommit("val2", now.Add(time.Second), 71, 1)
	appendCommit("val3", now.Add(2*time.Second), 71, 1)
	appendCommit("val4", now.Add(3*time.Second), 71, 1)
	appendCommit("val5", now.Add(4*time.Second), 71, 1)
	memStore.SetTip(71)

	updates := make(chan model.IncidentCard, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coord := &Coordinator{
		Source: &idleStubSource{},
		Store:  memStore,
		Genesis: model.Genesis{
			ChainID: "test-chain",
		},
		Sources: []model.Source{
			{Path: "/tmp/val1.log", Node: "val1", Role: model.RoleValidator},
			{Path: "/tmp/val2.log", Node: "val2", Role: model.RoleValidator},
			{Path: "/tmp/val3.log", Node: "val3", Role: model.RoleValidator},
			{Path: "/tmp/val4.log", Node: "val4", Role: model.RoleValidator},
			{Path: "/tmp/val5.log", Node: "val5", Role: model.RoleValidator},
		},
		Debounce:        5 * time.Millisecond,
		RefreshInterval: 10 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicyObservedValidatorMajority,
			ValidatorSources: []string{"val1", "val2", "val3", "val4", "val5"},
		},
		MaxHistory: 16,
		OnIncidentUpdate: func(card model.IncidentCard) {
			updates <- card
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- coord.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.ID == "stalled-validator-val1" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)

	cancel()
	err := <-done
	require.True(t, err == nil || err == context.Canceled)

	select {
	case update := <-updates:
		require.Equal(t, "stalled-validator", update.Kind)
		require.Equal(t, "active", update.Status)
		require.Equal(t, "stalled-validator-val1", update.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stalled-validator incident")
	}
}

func TestCoordinatorBuildsRuntimeValidatorOrderFromBufferedSignedVotes(t *testing.T) {
	memStore := store.NewMemoryStore(16)
	appendSignedVote := func(node, addr string, idx int, height int64, line int) {
		require.NoError(t, memStore.AppendEvent(model.Event{
			Timestamp:    time.Now().UTC(),
			HasTimestamp: true,
			Node:         node,
			Role:         model.RoleValidator,
			Path:         "/tmp/" + node + ".log",
			Line:         line,
			Kind:         model.EventSignedVote,
			Height:       height,
			Fields: map[string]any{
				"_vidx":             idx,
				"_vaddrprefix":      "IGNORED",
				"validator address": addr,
			},
		}))
	}

	appendSignedVote("val3", "g1zhmn5c3g4dhkdh52fn772wz3vl8naqlc8fzvwy", 0, 70, 1)
	appendSignedVote("val4", "g17whselle8ke692awdtcyvcneakgxw7qjryrszc", 1, 70, 1)
	appendSignedVote("val1", "g1jwczk2k625wtzlv4fudscxhya2lq5k6ksqfzyk", 2, 70, 1)
	appendSignedVote("val2", "g15k7zj4t8pxgedxs822lmj6eun2m9e6hmd6slf5", 3, 70, 1)
	appendSignedVote("val5", "g1g9lm70uteks5l0psrqrs0hfq63f0yraejfgj6r", 4, 70, 1)
	memStore.SetTip(70)

	coord := &Coordinator{
		Store: memStore,
		Genesis: model.Genesis{
			ChainID: "test-chain",
		},
		Sources: []model.Source{
			{Path: "/tmp/val1.log", Node: "val1", Role: model.RoleValidator},
			{Path: "/tmp/val2.log", Node: "val2", Role: model.RoleValidator},
			{Path: "/tmp/val3.log", Node: "val3", Role: model.RoleValidator},
			{Path: "/tmp/val4.log", Node: "val4", Role: model.RoleValidator},
			{Path: "/tmp/val5.log", Node: "val5", Role: model.RoleValidator},
		},
		resolver: &IdentityResolver{},
	}

	validators := coord.runtimeValidatorsFromBufferedEvents()
	require.Len(t, validators, 5)
	require.Equal(t, "g1zhmn5c3g4dhkdh52fn772wz3vl8naqlc8fzvwy", validators[0].Address)
	require.Equal(t, "g17whselle8ke692awdtcyvcneakgxw7qjryrszc", validators[1].Address)
	require.Equal(t, "g1jwczk2k625wtzlv4fudscxhya2lq5k6ksqfzyk", validators[2].Address)
	require.Equal(t, "g15k7zj4t8pxgedxs822lmj6eun2m9e6hmd6slf5", validators[3].Address)
	require.Equal(t, "g1g9lm70uteks5l0psrqrs0hfq63f0yraejfgj6r", validators[4].Address)
}

type stubSource struct {
	lines []source.Line
}

func (s *stubSource) Name() string {
	return "stub"
}

func (s *stubSource) Stream(ctx context.Context) (<-chan source.Line, <-chan error) {
	lines := make(chan source.Line, len(s.lines))
	errs := make(chan error)

	for _, line := range s.lines {
		lines <- line
	}
	close(lines)
	close(errs)

	return lines, errs
}

type delayedLine struct {
	line  source.Line
	delay time.Duration
}

type delayedStubSource struct {
	items []delayedLine
}

func (s *delayedStubSource) Name() string {
	return "delayed-stub"
}

func (s *delayedStubSource) Stream(ctx context.Context) (<-chan source.Line, <-chan error) {
	lines := make(chan source.Line)
	errs := make(chan error)

	go func() {
		defer close(lines)
		defer close(errs)

		for _, item := range s.items {
			if item.delay > 0 {
				timer := time.NewTimer(item.delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			select {
			case <-ctx.Done():
				return
			case lines <- item.line:
			}
		}
	}()

	return lines, errs
}

type idleStubSource struct{}

func (s *idleStubSource) Name() string {
	return "idle-stub"
}

func (s *idleStubSource) Stream(ctx context.Context) (<-chan source.Line, <-chan error) {
	lines := make(chan source.Line)
	errs := make(chan error)

	go func() {
		defer close(lines)
		defer close(errs)
		<-ctx.Done()
	}()

	return lines, errs
}

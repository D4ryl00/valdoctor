package live

// Scenario-based tests for the live Coordinator.
//
// Each test is derived from a real val-scenario (from /misc/val-scenarios) and
// verifies that the coordinator's incident detection logic matches the expected
// behaviour for that scenario.
//
// Scenario reference:
//   07 – 5 validators, reset 1 (4/5 > 2/3, chain keeps advancing)
//   08 – 5 validators, reset 2 (3/5 < 2/3, chain halts)
//   11 – 4 validators with weighted power (val1 alone holds >2/3 of power)

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/source"
	"github.com/D4ryl00/valdoctor/internal/store"
	"github.com/stretchr/testify/require"
)

// baseTS is a fixed past epoch used for all synthetic log timestamps.  Using a
// fixed past time means time.Now()-baseTS is always large (> any stallThreshold)
// so stall incidents trigger reliably without sleeps.
const baseTS = 1776333600 // 2026-04-14T06:00:00Z

func finalizeCommitLine(node string, height int64, ts int64) source.Line {
	return source.Line{
		Raw:    fmt.Sprintf(`{"level":"info","ts":%d,"msg":"Finalizing commit of block","height":%d}`, ts, height),
		Path:   fmt.Sprintf("/tmp/%s.log", node),
		Node:   node,
		Role:   model.RoleValidator,
		LineNo: int(height),
	}
}

func timeoutLine(node string, height int64, round int, ts int64) source.Line {
	return source.Line{
		Raw:    fmt.Sprintf(`{"level":"info","ts":%d,"msg":"Timed out","height":%d,"round":%d,"step":"RoundStepPropose"}`, ts, height, round),
		Path:   fmt.Sprintf("/tmp/%s.log", node),
		Node:   node,
		Role:   model.RoleValidator,
		LineNo: int(height)*100 + round,
	}
}

func switchToConsensusLine(node string, ts int64) source.Line {
	return source.Line{
		Raw:    fmt.Sprintf(`{"level":"info","ts":%d,"msg":"SwitchToConsensus"}`, ts),
		Path:   fmt.Sprintf("/tmp/%s.log", node),
		Node:   node,
		Role:   model.RoleValidator,
		LineNo: 1,
	}
}

// makeLines builds commit lines for every (node, height) pair in nodeHeights,
// spacing timestamps by 1 second starting from baseTS.
func makeLines(nodeHeights map[string][]int64) []source.Line {
	var lines []source.Line
	ts := int64(baseTS)
	// Sort deterministically to keep timestamps monotone across nodes
	for h := int64(1); h <= maxHeight(nodeHeights); h++ {
		for _, node := range sortedKeys(nodeHeights) {
			heights := nodeHeights[node]
			for _, nh := range heights {
				if nh == h {
					lines = append(lines, finalizeCommitLine(node, h, ts))
					ts++
					break
				}
			}
		}
	}
	return lines
}

func maxHeight(m map[string][]int64) int64 {
	var max int64
	for _, heights := range m {
		for _, h := range heights {
			if h > max {
				max = h
			}
		}
	}
	return max
}

func sortedKeys(m map[string][]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort for determinism without importing "sort" again.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func validatorSources(names ...string) []model.Source {
	srcs := make([]model.Source, len(names))
	for i, name := range names {
		srcs[i] = model.Source{
			Path: fmt.Sprintf("/tmp/%s.log", name),
			Node: name,
			Role: model.RoleValidator,
		}
	}
	return srcs
}

// TestScenario07StalledValidatorDetected reproduces scenario-07:
// 5 validators, val2 is stopped after height 5. The remaining 4 keep committing
// through height 10. Valdoctor must detect val2 as stalled.
//
// The fix exercised here: refreshNodeStates now measures StallDuration against
// time.Now() for validators behind the tip, so the stall fires even when val2
// produced no log events after its last commit.
func TestScenario07StalledValidatorDetected(t *testing.T) {
	validators := []string{"val1", "val2", "val3", "val4", "val5"}
	sources := validatorSources(validators...)

	// val2 commits heights 1–5 then stops.  Others commit 1–10.
	var lines []source.Line
	ts := int64(baseTS)
	for h := int64(1); h <= 5; h++ {
		for _, v := range validators {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}
	for h := int64(6); h <= 10; h++ {
		for _, v := range []string{"val1", "val3", "val4", "val5"} {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)
	incidents := make(chan model.IncidentCard, 32)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: validators,
		},
		MaxHistory: 500,
		OnIncidentUpdate: func(card model.IncidentCard) {
			incidents <- card
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "stalled-validator" && card.Scope == "val2" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "expected stalled-validator incident for val2")

	// Confirm the chain tip is at 10 and val1 is not stalled.
	require.EqualValues(t, 10, memStore.CurrentTip())
	for _, card := range memStore.ActiveIncidents() {
		require.NotEqual(t, "val1", card.Scope, "val1 should not be stalled")
	}
}

// TestScenario08TwoStalledValidatorsBelowConsensus reproduces scenario-08:
// 5 validators, val2 and val3 stop after height 6 (3/5 remain, which is below
// the 2/3 threshold). Valdoctor must detect both val2 and val3 as stalled.
func TestScenario08TwoStalledValidatorsBelowConsensus(t *testing.T) {
	validators := []string{"val1", "val2", "val3", "val4", "val5"}
	sources := validatorSources(validators...)

	// All 5 commit heights 1–6.  Only val1,val4,val5 commit heights 7–8.
	var lines []source.Line
	ts := int64(baseTS)
	for h := int64(1); h <= 6; h++ {
		for _, v := range validators {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}
	for h := int64(7); h <= 8; h++ {
		for _, v := range []string{"val1", "val4", "val5"} {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: validators,
		},
		MaxHistory: 500,
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		actives := memStore.ActiveIncidents()
		hasVal2, hasVal3 := false, false
		for _, card := range actives {
			if card.Kind == "stalled-validator" && card.Scope == "val2" {
				hasVal2 = true
			}
			if card.Kind == "stalled-validator" && card.Scope == "val3" {
				hasVal3 = true
			}
		}
		return hasVal2 && hasVal3
	}, 2*time.Second, 10*time.Millisecond, "expected stalled-validator incidents for val2 and val3")

	// val1, val4, val5 must not be stalled.
	for _, card := range memStore.ActiveIncidents() {
		if card.Kind == "stalled-validator" {
			require.NotEqual(t, "val1", card.Scope)
			require.NotEqual(t, "val4", card.Scope)
			require.NotEqual(t, "val5", card.Scope)
		}
	}
}

// TestScenario08RoundEscalationDetected verifies that consensus round escalation
// (reaching round >= 3 at a single height) produces a round-escalation incident,
// as would happen during the halt phase of scenario-08.
func TestScenario08RoundEscalationDetected(t *testing.T) {
	sources := validatorSources("val1", "val2", "val3", "val4", "val5")

	ts := int64(baseTS)
	lines := []source.Line{
		// Normal commits at heights 1–3.
		finalizeCommitLine("val1", 1, ts),
		finalizeCommitLine("val1", 2, ts+1),
		finalizeCommitLine("val1", 3, ts+2),
		// Chain stalls at height 4; consensus escalates to round 3.
		timeoutLine("val1", 4, 1, ts+3),
		timeoutLine("val1", 4, 2, ts+4),
		timeoutLine("val1", 4, 3, ts+5),
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"val1", "val2", "val3", "val4", "val5"},
		},
		MaxHistory: 500,
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "round-escalation" && card.Scope == "val1" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "expected round-escalation incident for val1")
}

// TestScenario11WeightedPowerSingleCommitPolicy reproduces scenario-11:
// 4 validators with unequal voting power (val1=10, val2-4=1 each).
// Val1 alone holds >2/3 of total power so it can commit blocks without val2-4.
// With PolicySingleValidatorCommit, heights close as soon as val1 reports —
// this is the correct policy when one validator dominates consensus.
// Val2-4 stopping should raise stalled-validator incidents for them.
func TestScenario11WeightedPowerSingleCommitPolicy(t *testing.T) {
	validators := []string{"val1", "val2", "val3", "val4"}
	sources := validatorSources(validators...)

	// All 4 commit heights 1–5.  Only val1 commits 6–10 (val2-4 stopped).
	var lines []source.Line
	ts := int64(baseTS)
	for h := int64(1); h <= 5; h++ {
		for _, v := range validators {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}
	for h := int64(6); h <= 10; h++ {
		lines = append(lines, finalizeCommitLine("val1", h, ts))
		ts++
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		// PolicySingleValidatorCommit is the right choice when one validator
		// dominates voting power: a single FinalizeCommit proves the height
		// committed regardless of how many validator log sources we observe.
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: validators,
		},
		MaxHistory: 500,
	}

	require.NoError(t, coord.Run(context.Background()))

	// Heights 6–10 must be closed even though only val1 reported.
	require.Eventually(t, func() bool {
		entry, ok := memStore.GetHeight(10)
		return ok && entry.Status == model.HeightClosed
	}, 2*time.Second, 10*time.Millisecond, "height 10 must close with single-commit policy")

	// Val2, val3, val4 must be detected as stalled.
	require.Eventually(t, func() bool {
		stalled := map[string]bool{}
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "stalled-validator" {
				stalled[card.Scope] = true
			}
		}
		return stalled["val2"] && stalled["val3"] && stalled["val4"]
	}, 2*time.Second, 10*time.Millisecond, "val2, val3, val4 must be stalled")

	// Val1 must not be stalled.
	for _, card := range memStore.ActiveIncidents() {
		if card.Kind == "stalled-validator" {
			require.NotEqual(t, "val1", card.Scope, "val1 (high-power) must not be stalled")
		}
	}
}

// TestScenario11MajorityPolicyMissesSingleDominantValidator documents that
// PolicyObservedValidatorMajority (count-based) cannot close heights when a
// single high-power validator is the only source, because it requires ⌊N*2/3⌋+1
// sources regardless of their actual voting power.
//
// Scenario-11 (val1 holds 10/13 power) must therefore use
// PolicySingleValidatorCommit or a future power-aware policy instead.
func TestScenario11MajorityPolicyMissesSingleDominantValidator(t *testing.T) {
	validators := []string{"val1", "val2", "val3", "val4"}
	sources := validatorSources(validators...)

	// Only val1 commits (simulating the scenario-11 post-halt phase).
	var lines []source.Line
	ts := int64(baseTS)
	for h := int64(1); h <= 5; h++ {
		lines = append(lines, finalizeCommitLine("val1", h, ts))
		ts++
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		// PolicyObservedValidatorMajority requires ⌊4*2/3⌋+1 = 3 sources; val1
		// alone (count=1) does not satisfy this, so heights never close.
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicyObservedValidatorMajority,
			ValidatorSources: validators,
		},
		MaxHistory: 500,
	}

	require.NoError(t, coord.Run(context.Background()))

	// Allow debounce to settle.
	time.Sleep(50 * time.Millisecond)

	// With count-based majority no height should be closed.
	for h := int64(1); h <= 5; h++ {
		entry, ok := memStore.GetHeight(h)
		if ok {
			require.Equal(t, model.HeightActive, entry.Status,
				"height %d must stay active with count-based majority when only val1 reports", h)
		}
	}
}

// TestScenario07StalledValidatorResolvesAfterRestart verifies that a
// stalled-validator incident is resolved once the stopped node resumes committing.
func TestScenario07StalledValidatorResolvesAfterRestart(t *testing.T) {
	validators := []string{"val1", "val2", "val3", "val4", "val5"}
	sources := validatorSources(validators...)

	ts := int64(baseTS)
	// Phase 1: all 5 commit heights 1–5.
	var lines []source.Line
	for h := int64(1); h <= 5; h++ {
		for _, v := range validators {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}
	// Phase 2: val2 stops; val1,3,4,5 commit heights 6–10.
	for h := int64(6); h <= 10; h++ {
		for _, v := range []string{"val1", "val3", "val4", "val5"} {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}
	// Phase 3: val2 restarts and catches up — commits heights 6–15 (fast-sync + resume).
	// First emit a SwitchToConsensus event to signal fast-sync completion.
	lines = append(lines, switchToConsensusLine("val2", ts))
	ts++
	for h := int64(6); h <= 15; h++ {
		lines = append(lines, finalizeCommitLine("val2", h, ts))
		ts++
	}
	// Phase 4: everyone commits heights 11–15.
	for h := int64(11); h <= 15; h++ {
		for _, v := range []string{"val1", "val3", "val4", "val5"} {
			lines = append(lines, finalizeCommitLine(v, h, ts))
			ts++
		}
	}

	src := &stubSource{lines: lines}
	memStore := store.NewMemoryStore(500)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: sources,
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: validators,
		},
		MaxHistory: 500,
	}

	require.NoError(t, coord.Run(context.Background()))

	// After all lines are processed, val2 has caught up to height 15 (same as tip).
	// The stalled-validator incident must not be active (either never raised or resolved).
	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "stalled-validator" && card.Scope == "val2" {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond, "stalled-validator for val2 must not be active after recovery")
}

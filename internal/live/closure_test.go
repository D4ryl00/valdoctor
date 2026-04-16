package live

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestClosureEvaluatorPolicies(t *testing.T) {
	now := time.Now().UTC()
	observedOne := map[string]time.Time{"validator-a": now}
	observedTwo := map[string]time.Time{"validator-a": now, "validator-b": now}
	observedThree := map[string]time.Time{"validator-a": now, "validator-b": now, "validator-c": now}
	observedFour := map[string]time.Time{"validator-a": now, "validator-b": now, "validator-c": now, "validator-d": now}

	single := ClosureEvaluator{
		Policy:           model.PolicySingleValidatorCommit,
		ValidatorSources: []string{"validator-a", "validator-b", "validator-c"},
	}
	require.True(t, single.ShouldClose(observedOne))

	majority := ClosureEvaluator{
		Policy:           model.PolicyObservedValidatorMajority,
		ValidatorSources: []string{"validator-a", "validator-b", "validator-c", "validator-d"},
	}
	require.False(t, majority.ShouldClose(observedOne))
	require.False(t, majority.ShouldClose(observedTwo))
	require.True(t, majority.ShouldClose(observedThree))

	all := ClosureEvaluator{
		Policy:           model.PolicyObservedAllValidatorSources,
		ValidatorSources: []string{"validator-a", "validator-b", "validator-c", "validator-d"},
	}
	require.False(t, all.ShouldClose(observedThree))
	require.True(t, all.ShouldClose(observedFour))
}

func TestClosureEvaluatorGracePassed(t *testing.T) {
	evaluator := ClosureEvaluator{GraceWindow: 5 * time.Second}
	closedAt := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)

	require.False(t, evaluator.GracePassed(closedAt, closedAt.Add(4*time.Second)))
	require.True(t, evaluator.GracePassed(closedAt, closedAt.Add(5*time.Second)))
}

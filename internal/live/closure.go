package live

import (
	"math"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type ClosureEvaluator struct {
	Policy           model.ClosurePolicy
	ValidatorSources []string
	GraceWindow      time.Duration
}

func (e ClosureEvaluator) ShouldClose(observed map[string]time.Time) bool {
	if len(observed) == 0 {
		return false
	}

	switch e.Policy {
	case model.PolicyObservedValidatorMajority:
		required := int(math.Floor(float64(len(e.ValidatorSources))*2.0/3.0)) + 1
		if required <= 0 {
			return false
		}
		return len(observed) >= required
	case model.PolicyObservedAllValidatorSources:
		if len(e.ValidatorSources) == 0 {
			return false
		}
		return len(observed) >= len(e.ValidatorSources)
	case model.PolicySingleValidatorCommit:
		fallthrough
	default:
		return len(observed) >= 1
	}
}

func (e ClosureEvaluator) GracePassed(closedAt, now time.Time) bool {
	if closedAt.IsZero() {
		return false
	}
	if e.GraceWindow <= 0 {
		return true
	}
	return !now.Before(closedAt.Add(e.GraceWindow))
}

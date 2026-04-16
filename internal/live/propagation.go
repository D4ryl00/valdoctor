package live

import (
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

const propagationLateThreshold = 500 * time.Millisecond

func BuildPropagation(
	events []model.Event,
	h int64,
	resolver *IdentityResolver,
	expectedReceivers []string,
	gracePassed bool,
) model.VotePropagation {
	prop := model.VotePropagation{
		Height: h,
		Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{},
	}

	expected := make(map[string]struct{}, len(expectedReceivers))
	for _, receiver := range expectedReceivers {
		expected[receiver] = struct{}{}
	}

	for _, event := range events {
		if event.Height != h {
			continue
		}
		if event.Kind != model.EventSignedVote {
			continue
		}

		key, castAt, ok := signedVoteKey(event, resolver)
		if !ok {
			continue
		}

		row := ensurePropagationRow(prop.Matrix, key, expectedReceivers)
		for receiver, receipt := range row {
			receipt.CastAt = castAt
			if receiver == key.OriginNode {
				receipt.ReceivedAt = castAt
			}
		}
	}

	for _, event := range events {
		if event.Height != h {
			continue
		}
		if event.Kind != model.EventAddedPrevote && event.Kind != model.EventAddedPrecommit {
			continue
		}

		key, receivedAt, ok := receivedVoteKey(event, resolver)
		if !ok {
			continue
		}

		row := ensurePropagationRow(prop.Matrix, key, expectedReceivers)
		receipt, tracked := row[event.Node]
		if !tracked {
			if _, wanted := expected[event.Node]; !wanted {
				continue
			}
			receipt = &model.VoteReceipt{}
			row[event.Node] = receipt
		}
		if receipt.ReceivedAt.IsZero() || receivedAt.Before(receipt.ReceivedAt) {
			receipt.ReceivedAt = receivedAt
		}
	}

	for _, row := range prop.Matrix {
		for _, receipt := range row {
			switch {
			case receipt.CastAt.IsZero():
				receipt.Status = "unknown_cast_time"
			case receipt.ReceivedAt.IsZero():
				if gracePassed {
					receipt.Status = "missing"
				}
			default:
				latency := receipt.ReceivedAt.Sub(receipt.CastAt)
				if latency < 0 {
					latency = 0
				}
				receipt.Latency = latency
				if latency >= propagationLateThreshold {
					receipt.Status = "late"
				} else {
					receipt.Status = "ok"
				}
			}
		}
	}

	return prop
}

func ensurePropagationRow(matrix map[model.VoteKey]map[string]*model.VoteReceipt, key model.VoteKey, expectedReceivers []string) map[string]*model.VoteReceipt {
	row := matrix[key]
	if row == nil {
		row = make(map[string]*model.VoteReceipt, len(expectedReceivers))
		for _, receiver := range expectedReceivers {
			row[receiver] = &model.VoteReceipt{}
		}
		matrix[key] = row
	}
	return row
}

func signedVoteKey(event model.Event, resolver *IdentityResolver) (model.VoteKey, time.Time, bool) {
	voteType, _ := event.Fields["_vote_type"].(string)
	if voteType == "" {
		return model.VoteKey{}, time.Time{}, false
	}

	identity, _ := resolver.ResolveByNode(event.Node)
	shortAddr := identity.ShortAddr
	if shortAddr == "" {
		shortAddr, _ = event.Fields["_vaddrprefix"].(string)
	}
	if shortAddr == "" {
		shortAddr = strings.ToUpper(event.Node)
	}

	castAt, _ := event.Fields["_cast_at"].(time.Time)
	if castAt.IsZero() {
		castAt = event.Timestamp
	}

	return model.VoteKey{
		Height:          event.Height,
		Round:           event.Round,
		VoteType:        voteType,
		OriginNode:      event.Node,
		OriginShortAddr: shortAddr,
	}, castAt, true
}

func receivedVoteKey(event model.Event, resolver *IdentityResolver) (model.VoteKey, time.Time, bool) {
	voteType, _ := event.Fields["_vote_type"].(string)
	if voteType == "" {
		return model.VoteKey{}, time.Time{}, false
	}

	prefix, _ := event.Fields["_vaddrprefix"].(string)
	if prefix == "" {
		return model.VoteKey{}, time.Time{}, false
	}

	identity, ok := resolver.ResolveByShortAddr(prefix)
	if !ok {
		identity = model.ValidatorIdentity{
			NodeName:  prefix,
			ShortAddr: prefix,
		}
	}

	receivedAt := event.Timestamp
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}

	return model.VoteKey{
		Height:          event.Height,
		Round:           event.Round,
		VoteType:        voteType,
		OriginNode:      identity.NodeName,
		OriginShortAddr: identity.ShortAddr,
	}, receivedAt, true
}

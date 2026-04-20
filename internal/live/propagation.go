package live

import (
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

const propagationLateThreshold = 500 * time.Millisecond

type receiverRoundKey struct {
	receiver string
	round    int
	voteType string
}

type receiverRoundState struct {
	maj23   bool
	maj23At time.Time
}

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

	type rtiKey struct {
		round    int
		voteType string
		index    int
	}
	rowByRTI := map[rtiKey]map[string]*model.VoteReceipt{}
	keyByRTI := map[rtiKey]model.VoteKey{}

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
		if idx, ok := event.Fields["_vidx"].(int); ok && idx >= 0 {
			rtik := rtiKey{round: event.Round, voteType: key.VoteType, index: idx}
			rowByRTI[rtik] = row
			keyByRTI[rtik] = key
		}
	}

	// Track which receiver nodes logged at least one vote receipt for this height.
	// A receiver with zero receipt events is running at INFO level (or lower) and
	// simply does not log individual vote receipts — we cannot distinguish
	// "vote not received" from "vote received but not logged".
	// We only mark receipts "missing" for receivers that have some receipt data,
	// because those receivers do log receipts and a gap is a genuine signal.
	receiversWithData := map[string]bool{}
	receiverRounds := map[receiverRoundKey]receiverRoundState{}
	for _, event := range events {
		if event.Height != h {
			continue
		}
		if event.Kind != model.EventAddedPrevote && event.Kind != model.EventAddedPrecommit {
			continue
		}

		voteType, _ := event.Fields["_vote_type"].(string)
		roundKey := receiverRoundKey{
			receiver: event.Node,
			round:    event.Round,
			voteType: voteType,
		}
		state := receiverRounds[roundKey]
		if maj23, _ := event.Fields["_vmaj23"].(bool); maj23 {
			state.maj23 = true
			if !event.Timestamp.IsZero() && (state.maj23At.IsZero() || event.Timestamp.Before(state.maj23At)) {
				state.maj23At = event.Timestamp
			}
		}
		receiverRounds[roundKey] = state

		key, receivedAt, ok := receivedVoteKey(event, resolver)
		row := map[string]*model.VoteReceipt(nil)
		if idx, hasIdx := event.Fields["_vidx"].(int); hasIdx && idx >= 0 {
			if mappedRow, found := rowByRTI[rtiKey{round: event.Round, voteType: voteType, index: idx}]; found {
				row = mappedRow
				key = keyByRTI[rtiKey{round: event.Round, voteType: voteType, index: idx}]
				ok = true
			}
		}
		if !ok {
			// Event lacks per-vote detail (e.g. gnoland debug logs only carry the
			// VoteSet BitArray, not individual vote origin). Don't mark this node
			// as having receipt data — we can't distinguish "not received" from
			// "received but not logged in this format".
			continue
		}
		// Only count as "has data" when we can actually resolve the vote origin.
		receiversWithData[event.Node] = true

		if row == nil {
			row = ensurePropagationRow(prop.Matrix, key, expectedReceivers)
		}
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

	// BitArray progression pass: infer per-vote receipt times when the log format
	// only provides VoteSet BitArray snapshots (no per-vote origin detail).
	// Each EventAddedPrevote/Precommit on receiver R at time T reports the running
	// vote-set state; a bit at position i that newly appears as 'x' relative to the
	// previous snapshot from R means genesis slot i's vote was received around T.
	if len(resolver.Genesis.Validators) > 0 {
		// Pre-build index (round, voteType, originNode) → row for O(1) lookup.
		type rtvKey struct {
			round    int
			voteType string
			origin   string
		}
		rowByRTV := make(map[rtvKey]map[string]*model.VoteReceipt, len(prop.Matrix))
		for k, row := range prop.Matrix {
			rowByRTV[rtvKey{k.Round, k.VoteType, k.OriginNode}] = row
		}

		// Running BA state per (receiver, round, voteType).
		type baKey struct {
			receiver string
			round    int
			voteType string
		}
		prevBA := map[baKey]string{}

		for _, ev := range events {
			if ev.Height != h || !ev.HasTimestamp {
				continue
			}
			if ev.Kind != model.EventAddedPrevote && ev.Kind != model.EventAddedPrecommit {
				continue
			}
			bits, _ := ev.Fields["_vbits"].(string)
			voteType, _ := ev.Fields["_vote_type"].(string)
			if bits == "" || voteType == "" {
				continue
			}

			bk := baKey{ev.Node, ev.Round, voteType}
			prev := prevBA[bk]
			prevBA[bk] = bits

			for i := 0; i < len(bits); i++ {
				if bits[i] != 'x' {
					continue
				}
				if i < len(prev) && prev[i] == 'x' {
					continue // already seen in an earlier snapshot
				}
				originName := ""
				if row, ok := rowByRTI[rtiKey{round: ev.Round, voteType: voteType, index: i}]; ok {
					if key, found := keyByRTI[rtiKey{round: ev.Round, voteType: voteType, index: i}]; found {
						originName = key.OriginNode
						receipt, ok := row[ev.Node]
						if !ok {
							if _, wanted := expected[ev.Node]; !wanted {
								continue
							}
							receipt = &model.VoteReceipt{}
							row[ev.Node] = receipt
						}
						if receipt.ReceivedAt.IsZero() || ev.Timestamp.Before(receipt.ReceivedAt) {
							receipt.ReceivedAt = ev.Timestamp
						}
						receiversWithData[ev.Node] = true
						continue
					}
				}
				originName = resolver.ResolveByGenesisIndex(i)
				if originName == "" || originName == ev.Node {
					continue // self-receipt or unresolvable
				}
				row, ok := rowByRTV[rtvKey{ev.Round, voteType, originName}]
				if !ok {
					continue // no signed-vote row for this origin
				}
				receipt, ok := row[ev.Node]
				if !ok {
					if _, wanted := expected[ev.Node]; !wanted {
						continue
					}
					receipt = &model.VoteReceipt{}
					row[ev.Node] = receipt
				}
				if receipt.ReceivedAt.IsZero() || ev.Timestamp.Before(receipt.ReceivedAt) {
					receipt.ReceivedAt = ev.Timestamp
				}
				receiversWithData[ev.Node] = true
			}
		}
	}

	for key, row := range prop.Matrix {
		for receiver, receipt := range row {
			roundState := receiverRounds[receiverRoundKey{
				receiver: receiver,
				round:    key.Round,
				voteType: key.VoteType,
			}]
			switch {
			case receipt.CastAt.IsZero():
				receipt.Status = "unknown_cast_time"
			case receipt.ReceivedAt.IsZero():
				// Only report "missing" when the receiver has other receipt events for
				// this height — meaning it does log receipts and this one is genuinely
				// absent. If the receiver has no receipt events at all, the log level is
				// too low to draw conclusions.
				if gracePassed && receiversWithData[receiver] {
					if roundState.maj23 {
						// Once the receiver already logged +2/3 precommits for the round,
						// or +2/3 prevotes that advance the round, an extra receipt that
						// never appears in the logs is not a reliable propagation miss
						// signal: TM2 often moves on immediately after quorum and never
						// emits a per-vote receipt for the surplus validator. Keep it
						// visible in the matrix, but suppress incidents.
						receipt.Status = "quorum-satisfied"
						break
					}
					receipt.Status = "missing"
				} else if gracePassed {
					// Grace passed but no receipt events from this node at all —
					// logs are at INFO level. Mark explicitly so the UI can show "-"
					// instead of the misleading "pending".
					receipt.Status = "no-data"
				}
			default:
				latency := receipt.ReceivedAt.Sub(receipt.CastAt)
				if latency < 0 {
					latency = 0
				}
				receipt.Latency = latency
				if roundState.maj23 && !roundState.maj23At.IsZero() && !roundState.maj23At.After(receipt.ReceivedAt) {
					// BitArray-based receipt inference often lands on or after the same
					// snapshot that already showed quorum. That is enough to reconstruct
					// the round outcome, but not enough to claim a meaningful
					// propagation delay for this specific vote.
					receipt.Status = "quorum-satisfied"
					break
				}
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

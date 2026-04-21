package live

import (
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type receiverRoundKey struct {
	receiver string
	round    int
	voteType string
}

type receiverRoundState struct {
	maj23   bool
	maj23At time.Time
	maj23Seq int
}

type receiptEvidence struct {
	firstSeq int
	bitSeq   int
}

func ensureReceiptEvidence(meta map[*model.VoteReceipt]*receiptEvidence, receipt *model.VoteReceipt) *receiptEvidence {
	if evidence, ok := meta[receipt]; ok {
		return evidence
	}
	evidence := &receiptEvidence{}
	meta[receipt] = evidence
	return evidence
}

func recordReceiptSequence(meta map[*model.VoteReceipt]*receiptEvidence, receipt *model.VoteReceipt, seq int, fromBitArray bool) {
	evidence := ensureReceiptEvidence(meta, receipt)
	if evidence.firstSeq == 0 || seq < evidence.firstSeq {
		evidence.firstSeq = seq
	}
	if fromBitArray && (evidence.bitSeq == 0 || seq < evidence.bitSeq) {
		evidence.bitSeq = seq
	}
}

func isPropagationEvidenceKind(kind model.EventKind) bool {
	switch kind {
	case model.EventAddedPrevote, model.EventAddedPrecommit, model.EventObservedPrevote, model.EventObservedPrecommit:
		return true
	default:
		return false
	}
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
	receiptMeta := map[*model.VoteReceipt]*receiptEvidence{}
	for idx, event := range events {
		if event.Height != h {
			continue
		}
		if !isPropagationEvidenceKind(event.Kind) {
			continue
		}

		voteType, _ := event.Fields["_vote_type"].(string)
		roundKey := receiverRoundKey{
			receiver: event.Node,
			round:    event.Round,
			voteType: voteType,
		}
		state := receiverRounds[roundKey]
		if event.Kind == model.EventAddedPrevote || event.Kind == model.EventAddedPrecommit {
			if maj23, _ := event.Fields["_vmaj23"].(bool); maj23 {
				state.maj23 = true
				if !event.Timestamp.IsZero() && (state.maj23At.IsZero() || event.Timestamp.Before(state.maj23At)) {
					state.maj23At = event.Timestamp
				}
				if state.maj23Seq == 0 {
					state.maj23Seq = idx + 1
				}
			}
			receiverRounds[roundKey] = state
		}

		key, receivedAt, ok := receivedVoteKey(event, resolver)
		row := map[string]*model.VoteReceipt(nil)
		if idx, hasIdx := event.Fields["_vidx"].(int); hasIdx && idx >= 0 {
			if mappedRow, found := rowByRTI[rtiKey{round: event.Round, voteType: voteType, index: idx}]; found {
				row = mappedRow
				key = keyByRTI[rtiKey{round: event.Round, voteType: voteType, index: idx}]
				if receivedAt.IsZero() {
					receivedAt = event.Timestamp
				}
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
		recordReceiptSequence(receiptMeta, receipt, idx+1, false)
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

		for idx, ev := range events {
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
						recordReceiptSequence(receiptMeta, receipt, idx+1, true)
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
				recordReceiptSequence(receiptMeta, receipt, idx+1, true)
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
				if gracePassed && roundState.maj23 {
					receipt.Status = "quorum-satisfied"
					break
				}
				// Only report "missing" when the receiver has other receipt events for
				// this height — meaning it does log receipts and this one is genuinely
				// absent. If the receiver has no receipt events at all, the log level is
				// too low to draw conclusions.
				if gracePassed && receiversWithData[receiver] {
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
				if roundState.maj23 && !roundState.maj23At.IsZero() {
					if evidence, ok := receiptMeta[receipt]; ok && evidence.bitSeq != 0 && roundState.maj23Seq != 0 {
						switch {
						case evidence.bitSeq > roundState.maj23Seq:
							receipt.Status = "late"
						default:
							// Prefer receiver-local VoteSet progression over wall-clock
							// timestamps: if the vote bit is already present before, or on,
							// the first +2/3 snapshot, it was in time for that receiver's
							// local quorum.
							receipt.Status = "ok"
						}
						break
					}
					switch {
					case receipt.ReceivedAt.After(roundState.maj23At):
						// If the first concrete receipt evidence for this vote lands after
						// the receiver's +2/3 snapshot, it was not needed to form quorum
						// on that receiver. Surface it as late and keep the latency.
						receipt.Status = "late"
					case receipt.ReceivedAt.Equal(roundState.maj23At):
						// Console timestamps are only millisecond-precise. A receipt that
						// lands in the same tick as the first +2/3 snapshot is
						// simultaneous/ambiguous, not provably late.
						receipt.Status = "quorum-satisfied"
					default:
						receipt.Status = "ok"
					}
					break
				}
				receipt.Status = "ok"
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
	var identity model.ValidatorIdentity
	var ok bool
	if prefix != "" {
		identity, ok = resolver.ResolveByShortAddr(prefix)
	}
	if !ok {
		if idx, hasIdx := event.Fields["_vidx"].(int); hasIdx && idx >= 0 {
			if nodeName := resolver.ResolveByGenesisIndex(idx); nodeName != "" {
				if resolved, found := resolver.ResolveByNode(nodeName); found {
					identity = resolved
					ok = true
				} else {
					identity = model.ValidatorIdentity{
						NodeName:     nodeName,
						GenesisIndex: idx,
						IsValidator:  true,
					}
					ok = true
				}
			}
		}
	}
	if !ok {
		return model.VoteKey{}, time.Time{}, false
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

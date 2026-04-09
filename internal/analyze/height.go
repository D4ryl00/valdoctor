package analyze

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/rpc"
)

// HeightInput collects all data sources for BuildHeightReport.
type HeightInput struct {
	Height   int64
	Genesis  model.Genesis
	Sources  []model.Source
	Metadata model.Metadata

	// Events pre-filtered by FilterEventsByHeight (contains height H, H-1 commit,
	// and all peer add/drop events with timestamps).
	Events []model.Event

	// RPC data — nil fields mean the data was unavailable (offline or fetch failed).
	Block      *rpc.BlockHeader
	CommitSigs []rpc.CommitSig // from /commit?height=H
	TxResults  []model.TxSummary

	// FocusNode, when non-empty, restricts the vote grid and peer table to
	// events from that specific node. Clock sync always covers all nodes.
	FocusNode string
}

// BuildHeightReport produces the full height analysis from log events and RPC data.
func BuildHeightReport(input HeightInput) model.HeightReport {
	report := model.HeightReport{
		Height:    input.Height,
		ChainID:   input.Genesis.ChainID,
		TxResults: input.TxResults,
		FocusNode: input.FocusNode,
	}

	// Translate RPC block header into model type and resolve proposer name.
	if input.Block != nil {
		bh := &model.BlockHeader{
			Time:         input.Block.Time,
			Hash:         input.Block.Hash,
			ProposerAddr: input.Block.ProposerAddr,
			TxCount:      input.Block.TxCount,
			AppHash:      input.Block.AppHash,
		}
		bh.ProposerName = resolveAddress(bh.ProposerAddr, input.Genesis, input.Metadata)
		report.Block = bh
	}

	// Translate RPC commit sigs into model type and resolve validator names.
	if input.CommitSigs != nil {
		sigs := make([]model.CommitSig, len(input.CommitSigs))
		for i, s := range input.CommitSigs {
			sigs[i] = model.CommitSig{
				Index:         s.ValidatorIndex,
				ValidatorAddr: s.ValidatorAddress,
				ValidatorName: resolveAddress(s.ValidatorAddress, input.Genesis, input.Metadata),
				Signed:        s.Signed,
				Round:         s.Round,
			}
			// If still not resolved, try genesis by index.
			if sigs[i].ValidatorName == "" && s.ValidatorIndex < len(input.Genesis.Validators) {
				sigs[i].ValidatorName = input.Genesis.Validators[s.ValidatorIndex].Name
				if sigs[i].ValidatorAddr == "" {
					sigs[i].ValidatorAddr = input.Genesis.Validators[s.ValidatorIndex].Address
				}
			}
		}
		report.CommitSigs = sigs
	}

	// Separate events into height-H events and peer events.
	var heightEvents []model.Event
	var peerEvents []model.Event
	var prevCommitTimes []time.Time // FinalizeCommit(H-1) timestamps per node

	for _, ev := range input.Events {
		switch {
		case ev.Height == input.Height:
			heightEvents = append(heightEvents, ev)
		case ev.Kind == model.EventFinalizeCommit && ev.Height == input.Height-1:
			if ev.HasTimestamp {
				prevCommitTimes = append(prevCommitTimes, ev.Timestamp)
			}
		case ev.Kind == model.EventAddedPeer || ev.Kind == model.EventStoppedPeer:
			peerEvents = append(peerEvents, ev)
		}
	}

	// Build the peer window: from the earliest H-1 commit (or EnterPropose H/0)
	// to the latest FinalizeCommit(H).
	windowStart, windowEnd := computeBlockWindow(heightEvents, prevCommitTimes)
	report.PeerWindowStart = windowStart
	report.PeerWindowEnd = windowEnd

	// Clock sync uses all node observations (not filtered by FocusNode).
	report.ClockSync = buildClockSync(heightEvents, input.Sources)

	// Remaining analysis may be restricted to a single node.
	analysisEvents := heightEvents
	if input.FocusNode != "" {
		analysisEvents = filterByNode(heightEvents, input.FocusNode)
	}

	report.Rounds = buildRoundSummaries(analysisEvents, input.Height)
	report.ValidatorVotes = buildValidatorVoteGrid(analysisEvents, input.Genesis)

	// Peer events always use the full set but are windowed.
	peerSource := peerEvents
	if input.FocusNode != "" {
		peerSource = filterByNode(peerEvents, input.FocusNode)
	}
	report.PeerEvents = buildPeerEvents(peerSource, windowStart, windowEnd, input.Metadata)

	// Double-sign detection.
	for _, ev := range analysisEvents {
		if ev.Kind == model.EventConflictingVote {
			report.DoubleSignDetected = true
			break
		}
	}

	return report
}

// ── helpers ──────────────────────────────────────────────────────────────────

func filterByNode(events []model.Event, node string) []model.Event {
	out := make([]model.Event, 0, len(events))
	for _, ev := range events {
		if ev.Node == node {
			out = append(out, ev)
		}
	}
	return out
}

// resolveAddress maps a raw address string (hex or bech32) to a human-readable
// node/validator name. It first checks the metadata nodes, then falls back to
// the genesis validator list. Returns empty string if not resolvable.
func resolveAddress(addr string, genesis model.Genesis, meta model.Metadata) string {
	if addr == "" {
		return ""
	}
	addrLower := strings.ToLower(addr)
	for name, node := range meta.Nodes {
		if strings.ToLower(node.ValidatorAddress) == addrLower {
			return name
		}
	}
	for _, val := range genesis.Validators {
		if strings.ToLower(val.Address) == addrLower {
			if val.Name != "" {
				return val.Name
			}
		}
	}
	return ""
}

// computeBlockWindow returns the start and end timestamps for the block period.
// Start: earliest EnterPropose(H, 0) across nodes OR earliest FinalizeCommit(H-1).
// End:   latest FinalizeCommit(H) across nodes.
func computeBlockWindow(heightEvents []model.Event, prevCommitTimes []time.Time) (start, end time.Time) {
	for _, ev := range heightEvents {
		if !ev.HasTimestamp {
			continue
		}
		if ev.Kind == model.EventEnterPropose && ev.Round == 0 {
			if start.IsZero() || ev.Timestamp.Before(start) {
				start = ev.Timestamp
			}
		}
		if ev.Kind == model.EventFinalizeCommit {
			if end.IsZero() || ev.Timestamp.After(end) {
				end = ev.Timestamp
			}
			if start.IsZero() || ev.Timestamp.Before(start) {
				start = ev.Timestamp
			}
		}
	}
	// Fall back to H-1 FinalizeCommit for window start if EnterPropose not observed.
	if start.IsZero() {
		for _, t := range prevCommitTimes {
			if start.IsZero() || t.Before(start) {
				start = t
			}
		}
	}
	return
}

// buildClockSync produces one ClockSyncRow per node that has either an
// EnterPropose(H, 0) or a FinalizeCommit(H) timestamp in the events.
func buildClockSync(events []model.Event, sources []model.Source) []model.ClockSyncRow {
	type nodeData struct {
		enterPropose   time.Time
		finalizeCommit time.Time
	}
	byNode := map[string]*nodeData{}

	// Pre-populate from sources so nodes with no events still appear (as unknown).
	for _, s := range sources {
		if _, ok := byNode[s.Node]; !ok {
			byNode[s.Node] = &nodeData{}
		}
	}

	for _, ev := range events {
		if !ev.HasTimestamp {
			continue
		}
		nd := byNode[ev.Node]
		if nd == nil {
			nd = &nodeData{}
			byNode[ev.Node] = nd
		}
		switch {
		case ev.Kind == model.EventEnterPropose && ev.Round == 0:
			if nd.enterPropose.IsZero() || ev.Timestamp.Before(nd.enterPropose) {
				nd.enterPropose = ev.Timestamp
			}
		case ev.Kind == model.EventFinalizeCommit:
			if nd.finalizeCommit.IsZero() || ev.Timestamp.After(nd.finalizeCommit) {
				nd.finalizeCommit = ev.Timestamp
			}
		}
	}

	// Compute median FinalizeCommit time across nodes that have one.
	var commitTimes []time.Time
	for _, nd := range byNode {
		if !nd.finalizeCommit.IsZero() {
			commitTimes = append(commitTimes, nd.finalizeCommit)
		}
	}
	sort.Slice(commitTimes, func(i, j int) bool { return commitTimes[i].Before(commitTimes[j]) })
	var medianCommit time.Time
	if n := len(commitTimes); n > 0 {
		medianCommit = commitTimes[n/2]
	}

	rows := make([]model.ClockSyncRow, 0, len(byNode))
	for name, nd := range byNode {
		row := model.ClockSyncRow{
			Node:               name,
			EnterProposeTime:   nd.enterPropose,
			FinalizeCommitTime: nd.finalizeCommit,
			Status:             "unknown",
		}
		if !nd.finalizeCommit.IsZero() && !medianCommit.IsZero() {
			row.DeltaMs = nd.finalizeCommit.Sub(medianCommit).Milliseconds()
			abs := row.DeltaMs
			if abs < 0 {
				abs = -abs
			}
			switch {
			case abs < 200:
				row.Status = "ok"
			case abs < 1000:
				row.Status = "warn"
			default:
				row.Status = "critical"
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Node < rows[j].Node })
	return rows
}

// buildRoundSummaries aggregates log events into per-round consensus summaries.
func buildRoundSummaries(events []model.Event, height int64) []model.RoundSummary {
	type roundData struct {
		proposalSeen      bool
		proposalHash      string
		proposalFromRound int
		proposalValid     bool

		// Best prevote snapshot seen for this round (latest by timestamp).
		pvRecv   int
		pvTotal  int
		pvMaj23  bool
		pvHash   string // block hash that got maj23, empty = nil
		pvHasAny bool

		// Best precommit snapshot.
		pcRecv   int
		pcTotal  int
		pcMaj23  bool
		pcHasAny bool

		prevoteNilSeen  bool   // EventPrevoteProposalNil observed
		precommitNo23   bool   // EventPrecommitNoMaj23 observed
		finalizeNo23    bool   // EventFinalizeNoMaj23 observed
		committed       bool   // EventFinalizeCommit observed
		timedOutPropose bool   // Timeout step=Propose
		timedOutPrevote bool   // Timeout step=Prevote
		timeoutPrevoteTS time.Time

		// Per-round individual vote counts (from _vbits bit arrays).
		// We track hash counts across individual EventAddedPrevote events to
		// compute "forBlock" vs "nil" counts when VoteSet maj23 info is absent.
		pvForBlockCount int
		pvNilCount      int
		pvOtherCount    int
		pcForBlockCount int
		pcNilCount      int
		pcOtherCount    int

		proposalHashForRound string // the hash of the block proposed this round
	}

	roundMap := map[int]*roundData{}
	ensureRound := func(r int) *roundData {
		if roundMap[r] == nil {
			roundMap[r] = &roundData{}
		}
		return roundMap[r]
	}

	for _, ev := range events {
		if ev.Height != height {
			continue
		}
		rd := ensureRound(ev.Round)

		switch ev.Kind {
		case model.EventEnterPropose:
			// proposer info is attached but proposal visibility is confirmed
			// only when the block part is received or signed.

		case model.EventSignedProposal:
			rd.proposalSeen = true
			rd.proposalValid = true
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				rd.proposalHash = shortHash(h)
				rd.proposalHashForRound = h
			}

		case model.EventReceivedCompletePart:
			rd.proposalSeen = true
			rd.proposalValid = true
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				rd.proposalHash = shortHash(h)
				rd.proposalHashForRound = h
			}
			if polRound, ok := ev.Fields["proposal_pol_round"].(float64); ok && polRound >= 0 {
				rd.proposalFromRound = int(polRound)
			}

		case model.EventPrevoteProposalNil:
			rd.prevoteNilSeen = true

		case model.EventPrevoteProposalInvalid:
			rd.proposalSeen = true
			rd.proposalValid = false

		case model.EventPrecommitNoMaj23:
			rd.precommitNo23 = true

		case model.EventFinalizeNoMaj23:
			rd.finalizeNo23 = true

		case model.EventFinalizeCommit:
			rd.committed = true
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				rd.proposalHash = shortHash(h)
			}

		case model.EventTimeout:
			step, _ := ev.Fields["_step"].(string)
			switch strings.ToLower(step) {
			case "propose":
				rd.timedOutPropose = true
			case "prevote", "prevotewait":
				rd.timedOutPrevote = true
				if ev.HasTimestamp && rd.timeoutPrevoteTS.IsZero() {
					rd.timeoutPrevoteTS = ev.Timestamp
				}
			}
			rd.timedOutPropose = rd.timedOutPropose || strings.ToLower(step) == "propose"

		case model.EventAddedPrevote:
			rd.pvHasAny = true
			if total, ok := ev.Fields["_vtotal"].(int); ok && total > 0 {
				if total > rd.pvTotal || (total == rd.pvTotal && ev.Fields["_vrecv"].(int) > rd.pvRecv) {
					rd.pvTotal = total
					rd.pvRecv, _ = ev.Fields["_vrecv"].(int)
					rd.pvMaj23, _ = ev.Fields["_vmaj23"].(bool)
					if rd.pvMaj23 {
						bits, _ := ev.Fields["_vbits"].(string)
						_ = bits
						// maj23 hash is the round's proposal block when _vmaj23=true
						rd.pvHash = rd.proposalHashForRound
					}
				}
			}
			// Track individual vote detail.
			if hash, ok := ev.Fields["_vhash"].(string); ok {
				if hash == "" {
					rd.pvNilCount++
				} else if rd.proposalHashForRound != "" && !strings.EqualFold(hash, rd.proposalHashForRound) {
					rd.pvOtherCount++
				} else {
					rd.pvForBlockCount++
				}
			}

		case model.EventAddedPrecommit:
			rd.pcHasAny = true
			if total, ok := ev.Fields["_vtotal"].(int); ok && total > 0 {
				if total > rd.pcTotal || (total == rd.pcTotal && ev.Fields["_vrecv"].(int) > rd.pcRecv) {
					rd.pcTotal = total
					rd.pcRecv, _ = ev.Fields["_vrecv"].(int)
					rd.pcMaj23, _ = ev.Fields["_vmaj23"].(bool)
				}
			}
			if hash, ok := ev.Fields["_vhash"].(string); ok {
				if hash == "" {
					rd.pcNilCount++
				} else if rd.proposalHashForRound != "" && !strings.EqualFold(hash, rd.proposalHashForRound) {
					rd.pcOtherCount++
				} else {
					rd.pcForBlockCount++
				}
			}
		}
	}

	// Sort rounds and build RoundSummary objects.
	rounds := make([]int, 0, len(roundMap))
	for r := range roundMap {
		rounds = append(rounds, r)
	}
	sort.Ints(rounds)

	result := make([]model.RoundSummary, 0, len(rounds))
	for _, r := range rounds {
		rd := roundMap[r]
		rs := model.RoundSummary{
			Round:              r,
			ProposalSeen:       rd.proposalSeen,
			ProposalHash:       rd.proposalHash,
			ProposalFromRound:  rd.proposalFromRound,
			ProposalValid:      rd.proposalValid,
			PrevotesMaj23:      rd.pvMaj23,
			PrevoteMaj23Hash:   rd.pvHash,
			PrevotesTotal:      rd.pvTotal,
			PrevotesForBlock:   rd.pvForBlockCount,
			PrevotesNil:        rd.pvNilCount,
			PrevotesOther:      rd.pvOtherCount,
			PrecommitsMaj23:    rd.pcMaj23,
			PrecommitsTotal:    rd.pcTotal,
			PrecommitsForBlock: rd.pcForBlockCount,
			PrecommitsNil:      rd.pcNilCount,
			PrecommitsOther:    rd.pcOtherCount,
			Committed:          rd.committed,
			TimedOut:           rd.timedOutPropose || rd.timedOutPrevote,
		}
		rs.PrevoteNarrative = buildPrevoteNarrative(rd.proposalSeen, rd.timedOutPropose,
			rd.prevoteNilSeen, rd.pvMaj23, rd.pvHash, rd.pvRecv, rd.pvTotal)
		rs.PrecommitNarrative = buildPrecommitNarrative(rd.precommitNo23, rd.finalizeNo23,
			rd.committed, rd.pcMaj23, rd.pvMaj23, rd.timedOutPrevote, rd.pcRecv, rd.pcTotal)
		result = append(result, rs)
	}
	return result
}

func buildPrevoteNarrative(proposalSeen, timedOutPropose, prevoteNilSeen, maj23 bool,
	maj23Hash string, recv, total int) string {
	if !proposalSeen || timedOutPropose {
		return "+2/3 nil"
	}
	if maj23 {
		if maj23Hash != "" {
			return fmt.Sprintf("+2/3 block (%s…)", maj23Hash)
		}
		return "+2/3 nil"
	}
	if prevoteNilSeen {
		return "nil (no proposal received before timeout)"
	}
	if total > 0 {
		return fmt.Sprintf("no +2/3 — split observed (%d/%d for block)", recv, total)
	}
	return "no +2/3 majority"
}

func buildPrecommitNarrative(noMaj23, finalizeNo23, committed, pcMaj23, pvMaj23,
	prevoteTimedOut bool, recv, total int) string {
	if committed {
		return "+2/3 block"
	}
	if noMaj23 || finalizeNo23 {
		if pvMaj23 && prevoteTimedOut {
			return "+2/3 nil (POL formed too late in this round)"
		}
		return "+2/3 nil"
	}
	if pcMaj23 {
		return "+2/3 block (locked)"
	}
	if total > 0 {
		return fmt.Sprintf("+2/3 nil (%d/%d block at decision time)", recv, total)
	}
	return "+2/3 nil"
}

// buildValidatorVoteGrid builds one ValidatorVoteRow per genesis validator.
// It scans EventAddedPrevote/Precommit for _vidx + _vhash and classifies each
// vote as block / nil / other_block / late_block / late_nil / absent.
func buildValidatorVoteGrid(events []model.Event, genesis model.Genesis) []model.ValidatorVoteRow {
	// Build the row slice indexed by genesis position.
	rows := make([]model.ValidatorVoteRow, len(genesis.Validators))
	for i, val := range genesis.Validators {
		rows[i] = model.ValidatorVoteRow{
			Index:   i,
			Name:    val.Name,
			Addr:    val.Address,
			ByRound: map[int]model.VoteEntry{},
		}
	}

	// Determine the proposal block hash per round (used to classify "other block").
	proposalHashByRound := map[int]string{}
	for _, ev := range events {
		if ev.Kind == model.EventReceivedCompletePart || ev.Kind == model.EventSignedProposal {
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				proposalHashByRound[ev.Round] = h
			}
		}
	}

	// Collect timeout timestamps per round for "late" classification.
	timeoutByRound := map[int]time.Time{}
	for _, ev := range events {
		if ev.Kind == model.EventTimeout && ev.HasTimestamp {
			step, _ := ev.Fields["_step"].(string)
			if strings.ToLower(step) == "prevote" || strings.ToLower(step) == "prevotewait" {
				if t, ok := timeoutByRound[ev.Round]; ok {
					if ev.Timestamp.Before(t) {
						timeoutByRound[ev.Round] = ev.Timestamp
					}
				} else {
					timeoutByRound[ev.Round] = ev.Timestamp
				}
			}
		}
	}

	// Process prevote/precommit events.
	for _, ev := range events {
		if ev.Kind != model.EventAddedPrevote && ev.Kind != model.EventAddedPrecommit {
			continue
		}
		vidx, ok := ev.Fields["_vidx"].(int)
		if !ok || vidx < 0 || vidx >= len(rows) {
			continue
		}
		hash, _ := ev.Fields["_vhash"].(string)
		round := ev.Round

		var kind model.VoteKind
		switch {
		case hash == "":
			kind = model.VoteNil
			// Check if this nil vote arrived late (after prevote timeout).
			if t, hasTimeout := timeoutByRound[round]; hasTimeout && ev.HasTimestamp && ev.Timestamp.After(t) {
				kind = model.VoteLateNil
			}
		default:
			propHash := proposalHashByRound[round]
			if propHash != "" && !strings.EqualFold(hash, propHash) {
				kind = model.VoteOtherBlock
			} else {
				kind = model.VoteBlock
			}
			if t, hasTimeout := timeoutByRound[round]; hasTimeout && ev.HasTimestamp && ev.Timestamp.After(t) && kind == model.VoteBlock {
				kind = model.VoteLateBlock
			}
		}

		entry := rows[vidx].ByRound[round]
		if ev.Kind == model.EventAddedPrevote {
			// Only update if we haven't recorded a vote yet, or if this is more
			// informative (block > nil > absent).
			if entry.Prevote == "" || voteKindWeight(kind) > voteKindWeight(entry.Prevote) {
				entry.Prevote = kind
			}
		} else {
			if entry.Precommit == "" || voteKindWeight(kind) > voteKindWeight(entry.Precommit) {
				entry.Precommit = kind
			}
		}
		rows[vidx].ByRound[round] = entry
	}

	// Fill "absent" for validators with no vote observed in rounds where we do
	// have vote data (i.e., at least one other validator's vote was seen).
	roundsWithData := map[int]bool{}
	for _, ev := range events {
		if ev.Kind == model.EventAddedPrevote || ev.Kind == model.EventAddedPrecommit {
			roundsWithData[ev.Round] = true
		}
	}
	for r := range roundsWithData {
		for i := range rows {
			entry := rows[i].ByRound[r]
			if entry.Prevote == "" {
				entry.Prevote = model.VoteAbsent
			}
			if entry.Precommit == "" {
				entry.Precommit = model.VoteAbsent
			}
			rows[i].ByRound[r] = entry
		}
	}

	return rows
}

// voteKindWeight returns a numeric priority so that more informative vote kinds
// take precedence when multiple events are observed for the same validator.
func voteKindWeight(k model.VoteKind) int {
	switch k {
	case model.VoteBlock, model.VoteLateBlock, model.VoteOtherBlock:
		return 3
	case model.VoteNil, model.VoteLateNil:
		return 2
	case model.VoteAbsent:
		return 1
	}
	return 0
}

// buildPeerEvents filters peer add/drop events to those within [windowStart, windowEnd]
// and resolves peer identities from metadata.
func buildPeerEvents(events []model.Event, windowStart, windowEnd time.Time, meta model.Metadata) []model.PeerEvent {
	// Build a lookup: bech32 peer address → node name + role.
	type peerInfo struct {
		name string
		role model.Role
	}
	addrToInfo := map[string]peerInfo{}
	for name, node := range meta.Nodes {
		if node.NodeID != "" {
			addrToInfo[strings.ToLower(node.NodeID)] = peerInfo{name, model.ParseRole(node.Role)}
		}
	}
	for alias, name := range meta.PeerAliases {
		if info, ok := addrToInfo[strings.ToLower(name)]; ok {
			addrToInfo[strings.ToLower(alias)] = info
		} else {
			addrToInfo[strings.ToLower(alias)] = peerInfo{name: name}
		}
	}

	var result []model.PeerEvent
	for _, ev := range events {
		if ev.Kind != model.EventAddedPeer && ev.Kind != model.EventStoppedPeer {
			continue
		}
		if !ev.HasTimestamp {
			continue
		}
		// Window filter: skip if window is known and event is outside it.
		if !windowStart.IsZero() && ev.Timestamp.Before(windowStart) {
			continue
		}
		if !windowEnd.IsZero() && ev.Timestamp.After(windowEnd) {
			continue
		}

		paddr, _ := ev.Fields["_paddr"].(string)
		perr, _ := ev.Fields["_perr"].(string)

		pe := model.PeerEvent{
			Timestamp: ev.Timestamp,
			Node:      ev.Node,
			Added:     ev.Kind == model.EventAddedPeer,
			PeerAddr:  paddr,
			ErrReason: perr,
		}
		if info, ok := addrToInfo[strings.ToLower(paddr)]; ok {
			pe.PeerName = info.name
			pe.PeerRole = info.role
		}
		result = append(result, pe)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result
}

// shortHash returns the first 8 hex characters of a block hash for display.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

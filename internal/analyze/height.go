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
	Block             *rpc.BlockHeader
	CommitSigs        []rpc.CommitSig    // from /commit?height=H
	TxResults         []model.TxSummary
	RuntimeValidators []rpc.ValidatorEntry // from /validators?height=H; nil when offline or unavailable

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

	var validatorSetSize int
	report.Rounds, validatorSetSize = buildRoundSummaries(analysisEvents, input.Height)
	report.ValidatorSetSize = validatorSetSize
	for _, rs := range report.Rounds {
		if rs.Committed {
			report.CommittedInLog = true
			break
		}
	}
	report.ValidatorVotes = buildValidatorVoteGrid(analysisEvents, input.Genesis, input.Metadata, input.RuntimeValidators)

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
// Returns the summaries and the runtime validator set size (max VoteSet total seen).
func buildRoundSummaries(events []model.Event, height int64) ([]model.RoundSummary, int) {
	type roundData struct {
		proposerAddr      string
		proposalSeen      bool
		proposalHash      string
		proposalFromRound int
		proposalValid     bool
		proposalSeenTS    time.Time // when the complete block was first received

		// Best prevote snapshot seen for this round (highest recv count).
		pvRecv   int
		pvTotal  int
		pvMaj23  bool
		pvHash   string // block hash that got maj23, empty = nil
		pvHasAny bool

		// Best precommit snapshot.
		pcRecv   int
		pcTotal  int
		pcMaj23  bool
		pcHash   string // block hash that got maj23, empty = nil majority
		pcHasAny bool

		prevoteNilSeen   bool      // EventPrevoteProposalNil observed
		prevoteNilTS     time.Time // when it happened (for late-proposal detection)
		precommitNo23    bool      // EventPrecommitNoMaj23 observed
		finalizeNo23     bool      // EventFinalizeNoMaj23 observed
		committed        bool      // EventFinalizeCommit observed
		timedOutPropose  bool      // RoundStepPropose timeout observed
		timedOutPrevote  bool      // RoundStepPrevote timeout observed
		timeoutPrevoteTS time.Time

		// Per-round individual vote counts (from _vidx/_vhash on prevote events).
		pvForBlockCount int
		pvNilCount      int
		pvOtherCount    int
		pcForBlockCount int
		pcNilCount      int
		pcOtherCount    int

		proposalHashForRound string // full hex hash of the proposed block
	}

	roundMap := map[int]*roundData{}
	ensureRound := func(r int) *roundData {
		if roundMap[r] == nil {
			roundMap[r] = &roundData{}
		}
		return roundMap[r]
	}

	maxValidatorSetSize := 0
	// lastActiveRound tracks the highest round we've seen real consensus events for.
	// Used to assign FinalizeCommit (which has no round field) to the correct round.
	lastActiveRound := 0

	for _, ev := range events {
		if ev.Height != height {
			continue
		}
		// FinalizeCommit ("Finalizing commit of block") carries no round field and
		// defaults to round 0. Use the last actively-seen round instead.
		round := ev.Round
		if ev.Kind == model.EventFinalizeCommit {
			round = lastActiveRound
		} else if ev.Round > lastActiveRound {
			lastActiveRound = ev.Round
		}
		rd := ensureRound(round)

		switch ev.Kind {
		case model.EventEnterPropose:
			if addr, ok := ev.Fields["proposer"].(string); ok && addr != "" && rd.proposerAddr == "" {
				rd.proposerAddr = addr
			}

		case model.EventReceivedProposal:
			// "Received proposal" carries the correct height+round+hash. Use it as
			// the authoritative source for proposal identity.
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				rd.proposalSeen = true
				rd.proposalValid = true
				rd.proposalHash = shortHash(h)
				rd.proposalHashForRound = strings.ToUpper(h)
			}
			// POL round: a proposal re-using a block from an earlier round.
			if polRound, ok := ev.Fields["proposal round"].(float64); ok && polRound >= 0 {
				rd.proposalFromRound = int(polRound)
			}
			if ev.HasTimestamp && rd.proposalSeenTS.IsZero() {
				rd.proposalSeenTS = ev.Timestamp
			}

		case model.EventSignedProposal:
			rd.proposalSeen = true
			rd.proposalValid = true
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				if rd.proposalHashForRound == "" {
					rd.proposalHash = shortHash(h)
					rd.proposalHashForRound = strings.ToUpper(h)
				}
			}
			if ev.HasTimestamp && rd.proposalSeenTS.IsZero() {
				rd.proposalSeenTS = ev.Timestamp
			}

		case model.EventReceivedCompletePart:
			// "Received complete proposal block" has no round field (defaults to 0).
			// Only use it to mark proposalSeen; don't overwrite the hash set by
			// EventReceivedProposal which has the correct round.
			rd.proposalSeen = true
			rd.proposalValid = true
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" && rd.proposalHashForRound == "" {
				rd.proposalHash = shortHash(h)
				rd.proposalHashForRound = strings.ToUpper(h)
			}
			if ev.HasTimestamp && rd.proposalSeenTS.IsZero() {
				rd.proposalSeenTS = ev.Timestamp
			}

		case model.EventPrevoteProposalNil:
			rd.prevoteNilSeen = true
			if ev.HasTimestamp && rd.prevoteNilTS.IsZero() {
				rd.prevoteNilTS = ev.Timestamp
			}

		case model.EventPrevoteProposalInvalid:
			rd.proposalSeen = true
			rd.proposalValid = false
			if ev.HasTimestamp && rd.proposalSeenTS.IsZero() {
				rd.proposalSeenTS = ev.Timestamp
			}

		case model.EventPrecommitNoMaj23:
			rd.precommitNo23 = true

		case model.EventFinalizeNoMaj23:
			rd.finalizeNo23 = true

		case model.EventFinalizeCommit:
			rd.committed = true
			// Do NOT overwrite proposalHash: it's already set from EventReceivedProposal.

		case model.EventTimeout:
			step, _ := ev.Fields["_step"].(string)
			stepLow := strings.ToLower(step)
			// RoundStepPropose/RoundStepPrevote/RoundStepPrevoteWait.
			// RoundStepNewHeight is a startup sync timeout; do NOT set TimedOut.
			switch {
			case strings.Contains(stepLow, "propose") && !strings.Contains(stepLow, "newheight"):
				rd.timedOutPropose = true
			case strings.Contains(stepLow, "prevote"):
				rd.timedOutPrevote = true
				if ev.HasTimestamp && rd.timeoutPrevoteTS.IsZero() {
					rd.timeoutPrevoteTS = ev.Timestamp
				}
			}

		case model.EventAddedPrevote:
			rd.pvHasAny = true
			if total, ok := ev.Fields["_vtotal"].(int); ok && total > 0 {
				if total > maxValidatorSetSize {
					maxValidatorSetSize = total
				}
				if total > rd.pvTotal || (total == rd.pvTotal && ev.Fields["_vrecv"].(int) > rd.pvRecv) {
					rd.pvTotal = total
					rd.pvRecv, _ = ev.Fields["_vrecv"].(int)
					rd.pvMaj23, _ = ev.Fields["_vmaj23"].(bool)
					if rd.pvMaj23 {
						rd.pvHash, _ = ev.Fields["_vmaj23hash"].(string)
					}
				}
			}
			if hash, ok := ev.Fields["_vhash"].(string); ok {
				if hash == "" {
					rd.pvNilCount++
				} else if rd.proposalHashForRound != "" && !hashPrefixMatch(hash, rd.proposalHashForRound) {
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
					if rd.pcMaj23 {
						rd.pcHash, _ = ev.Fields["_vmaj23hash"].(string)
					}
				}
			}
			if hash, ok := ev.Fields["_vhash"].(string); ok {
				if hash == "" {
					rd.pcNilCount++
				} else if rd.proposalHashForRound != "" && !hashPrefixMatch(hash, rd.proposalHashForRound) {
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

		// Detect late proposal: proposal received after the nil prevote was cast.
		proposalLate := false
		proposalLateTimeStr := ""
		if rd.proposalSeen && rd.prevoteNilSeen {
			if !rd.prevoteNilTS.IsZero() && !rd.proposalSeenTS.IsZero() {
				if rd.proposalSeenTS.After(rd.prevoteNilTS) {
					proposalLate = true
					proposalLateTimeStr = rd.proposalSeenTS.UTC().Format("15:04:05Z")
				}
			} else if rd.prevoteNilSeen {
				// No timestamps; if we saw nil prevote AND proposal, mark late
				// because ProposalNil fires only when there was no block at prevote time.
				proposalLate = true
			}
		}

		rs := model.RoundSummary{
			Round:                r,
			ProposerAddr:         rd.proposerAddr,
			ProposalSeen:         rd.proposalSeen,
			ProposalHash:         rd.proposalHash,
			ProposalFromRound:    rd.proposalFromRound,
			ProposalValid:        rd.proposalValid,
			ProposalReceivedLate: proposalLate,
			ProposalLateTimeStr:  proposalLateTimeStr,
			PrevotesMaj23:        rd.pvMaj23,
			PrevoteMaj23Hash:     rd.pvHash,
			PrevotesTotal:        rd.pvTotal,
			PrevotesForBlock:     rd.pvForBlockCount,
			PrevotesNil:          rd.pvNilCount,
			PrevotesOther:        rd.pvOtherCount,
			PrecommitsTotal:      rd.pcTotal,
			PrecommitsMaj23:      rd.pcMaj23,
			PrecommitsForBlock:   rd.pcForBlockCount,
			PrecommitsNil:        rd.pcNilCount,
			PrecommitsOther:      rd.pcOtherCount,
			PrecommitDataSeen:    rd.pcHasAny,
			Committed:            rd.committed,
			TimedOut:             rd.timedOutPropose || rd.timedOutPrevote,
		}
		rs.PrevoteNarrative = buildPrevoteNarrative(
			rd.proposalSeen, rd.prevoteNilSeen, rd.timedOutPropose,
			rd.pvMaj23, rd.pvHash, rd.pvRecv, rd.pvTotal,
		)
		rs.PrecommitNarrative = buildPrecommitNarrative(
			rd.precommitNo23, rd.finalizeNo23, rd.committed,
			rd.pcMaj23, rd.pcHash, rd.pvMaj23, rd.timedOutPrevote,
			rd.pcRecv, rd.pcTotal, rd.pcHasAny,
		)
		result = append(result, rs)
	}
	return result, maxValidatorSetSize
}

// buildPrevoteNarrative produces a human-readable summary of the prevote step outcome.
func buildPrevoteNarrative(proposalSeen, prevoteNilSeen, timedOutPropose,
	pvMaj23 bool, pvHash string, pvRecv, pvTotal int,
) string {
	if pvMaj23 {
		if pvHash != "" {
			return fmt.Sprintf("+2/3 block (%s…)", shortHash(pvHash))
		}
		return "+2/3 nil"
	}
	if !proposalSeen && timedOutPropose {
		return "nil (propose timeout — no proposal)"
	}
	if prevoteNilSeen && timedOutPropose {
		return "nil (propose timeout)"
	}
	if prevoteNilSeen {
		return "nil (no block)"
	}
	if pvTotal > 0 {
		return fmt.Sprintf("%d/%d (no +2/3)", pvRecv, pvTotal)
	}
	return "unknown"
}

// buildPrecommitNarrative produces a human-readable summary of the precommit step outcome.
// pcMaj23Hash is the block hash that achieved +2/3 (from VoteSet majority field);
// empty string means a nil majority was reached.
func buildPrecommitNarrative(noMaj23, finalizeNo23, committed,
	pcMaj23 bool, pcMaj23Hash string, pvMaj23, prevoteTimedOut bool,
	recv, total int, dataAvailable bool,
) string {
	if !dataAvailable {
		return "not reached"
	}
	if committed {
		return "+2/3 block"
	}
	if pcMaj23 {
		if pcMaj23Hash != "" {
			return fmt.Sprintf("+2/3 block (%s)", shortHash(pcMaj23Hash))
		}
		return "+2/3 nil"
	}
	if noMaj23 || finalizeNo23 {
		if pvMaj23 && prevoteTimedOut {
			return "+2/3 nil (POL too late)"
		}
		return "+2/3 nil"
	}
	if total > 0 {
		return fmt.Sprintf("%d/%d (no +2/3)", recv, total)
	}
	return "unknown"
}

// buildValidatorVoteGrid builds one ValidatorVoteRow per validator slot.
// When runtimeValidators is non-empty (from RPC /validators?height=H), those
// addresses are used for slot labeling and names are resolved from metadata/genesis.
// Without runtime validators, genesis order is used for the first N slots.
// In both cases names are left empty when not resolvable — they are not displayed.
func buildValidatorVoteGrid(events []model.Event, genesis model.Genesis, meta model.Metadata, runtimeValidators []rpc.ValidatorEntry) []model.ValidatorVoteRow {
	// Determine the minimum row count from observed _vidx values.
	maxIdx := len(genesis.Validators) - 1
	for _, ev := range events {
		if ev.Kind == model.EventAddedPrevote || ev.Kind == model.EventAddedPrecommit {
			if idx, ok := ev.Fields["_vidx"].(int); ok && idx > maxIdx {
				maxIdx = idx
			}
		}
	}
	// Runtime validator list from RPC is the authoritative slot count.
	if len(runtimeValidators) > maxIdx+1 {
		maxIdx = len(runtimeValidators) - 1
	}
	slotCount := maxIdx + 1

	// Build the row slice indexed by position.
	rows := make([]model.ValidatorVoteRow, slotCount)
	for i := range rows {
		rows[i] = model.ValidatorVoteRow{Index: i, ByRound: map[int]model.VoteEntry{}}
	}

	// Label rows with addresses and resolved names.
	if len(runtimeValidators) > 0 {
		// Use RPC-ordered runtime validator set (authoritative).
		// Only set a name when it can be resolved; leave blank otherwise.
		for i, val := range runtimeValidators {
			if i >= slotCount {
				break
			}
			rows[i].Addr = val.Address
			rows[i].Name = resolveAddress(val.Address, genesis, meta)
		}
	} else {
		// No RPC data: infer runtime slot → validator by matching the address
		// fingerprint embedded in vote log messages against genesis/metadata.
		// TM2 prints the first 6 bytes of the address as 12 uppercase hex chars.

		// Build idx → observed address prefix map from vote events.
		idxToAddrPrefix := map[int]string{}
		for _, ev := range events {
			if ev.Kind != model.EventAddedPrevote && ev.Kind != model.EventAddedPrecommit {
				continue
			}
			idx, ok := ev.Fields["_vidx"].(int)
			if !ok {
				continue
			}
			if _, seen := idxToAddrPrefix[idx]; seen {
				continue // already have a prefix for this slot
			}
			if prefix, ok := ev.Fields["_vaddrprefix"].(string); ok && prefix != "" {
				idxToAddrPrefix[idx] = prefix
			}
		}

		// Build address-prefix → (name, addr) lookup from genesis and metadata.
		type valInfo struct{ name, addr string }
		prefixToInfo := map[string]valInfo{}
		for _, val := range genesis.Validators {
			if p := bech32HexPrefix(val.Address); p != "" {
				prefixToInfo[p] = valInfo{val.Name, val.Address}
			}
		}
		for name, node := range meta.Nodes {
			if node.ValidatorAddress == "" {
				continue
			}
			if p := bech32HexPrefix(node.ValidatorAddress); p != "" {
				if _, exists := prefixToInfo[p]; !exists {
					prefixToInfo[p] = valInfo{name, node.ValidatorAddress}
				}
			}
		}

		// Label rows where the address prefix resolves to a known validator.
		for i := range rows {
			prefix, ok := idxToAddrPrefix[i]
			if !ok {
				continue
			}
			if info, ok := prefixToInfo[prefix]; ok {
				rows[i].Name = info.name
				rows[i].Addr = info.addr
			}
		}
	}

	// Determine the proposal block hash per round (used to classify "other block").
	// EventReceivedProposal has the correct round; fall back to others only when absent.
	proposalHashByRound := map[int]string{}
	for _, ev := range events {
		switch ev.Kind {
		case model.EventReceivedProposal:
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				proposalHashByRound[ev.Round] = h
			}
		case model.EventReceivedCompletePart, model.EventSignedProposal:
			// Only use if no EventReceivedProposal already set this round's hash.
			if h, ok := ev.Fields["block_hash"].(string); ok && h != "" {
				if proposalHashByRound[ev.Round] == "" {
					proposalHashByRound[ev.Round] = h
				}
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
			if propHash != "" && !hashPrefixMatch(hash, propHash) {
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

// bech32HexPrefix decodes a bech32 address string and returns the first 6 raw
// bytes as 12 uppercase hex chars. This matches TM2's Fingerprint format, which
// is what vote log messages show for validator addresses (e.g. Vote{0:C6B6F63D1D3B…}).
// Returns empty string on any decode failure.
func bech32HexPrefix(addr string) string {
	// Locate the bech32 separator: the last '1' in the string.
	sep := strings.LastIndexByte(addr, '1')
	if sep < 1 || sep+7 > len(addr) { // need at least HRP + '1' + 1 data char + 6-char checksum
		return ""
	}
	dataStr := strings.ToLower(addr[sep+1:])
	if len(dataStr) <= 6 { // must have data beyond the 6-char checksum
		return ""
	}
	dataStr = dataStr[:len(dataStr)-6] // strip checksum

	// bech32 alphabet → 5-bit value.
	const alpha = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	charVal := func(c byte) int {
		for i := 0; i < len(alpha); i++ {
			if alpha[i] == c {
				return i
			}
		}
		return -1
	}

	// Convert 5-bit groups to 8-bit bytes.
	acc, bits := 0, 0
	var raw []byte
	for i := 0; i < len(dataStr); i++ {
		v := charVal(dataStr[i])
		if v < 0 {
			return ""
		}
		acc = (acc << 5) | v
		bits += 5
		for bits >= 8 {
			bits -= 8
			raw = append(raw, byte(acc>>bits))
		}
	}
	if len(raw) < 6 {
		return ""
	}
	return fmt.Sprintf("%X", raw[:6])
}

// shortHash returns the first 8 hex characters of a block hash for display.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// hashPrefixMatch returns true when a and b refer to the same block.
// TM2 vote strings truncate the block hash to 6 bytes (12 hex chars) while
// stored hashes may be longer; we accept a match when one is a prefix of the other.
func hashPrefixMatch(a, b string) bool {
	a = strings.ToUpper(a)
	b = strings.ToUpper(b)
	if len(a) <= len(b) {
		return strings.HasPrefix(b, a)
	}
	return strings.HasPrefix(a, b)
}

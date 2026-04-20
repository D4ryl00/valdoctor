package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

// HeightText renders a HeightReport as a human-readable text report.
func HeightText(report model.HeightReport, color bool) string {
	var b strings.Builder
	c := colorizer{color}

	// ── Header ───────────────────────────────────────────────────────────────
	fmt.Fprintf(&b, "%s — chain %s\n", c.bold(fmt.Sprintf("Height %d", report.Height)), report.ChainID)
	if report.Block != nil {
		bk := report.Block
		ts := ""
		if !bk.Time.IsZero() {
			ts = bk.Time.UTC().Format("2006-01-02T15:04:05Z") + "  "
		}
		proposer := bk.ProposerAddr
		if bk.ProposerName != "" {
			proposer = fmt.Sprintf("%s (%s)", bk.ProposerName, shortAddrDisplay(bk.ProposerAddr))
		}
		fmt.Fprintf(&b, "Block: %sproposer: %s  txs: %d  app_hash: %s\n",
			ts, proposer, bk.TxCount, shortAddrDisplay(bk.AppHash))
	}
	if report.FocusNode != "" {
		fmt.Fprintf(&b, "%s\n", c.yellow(fmt.Sprintf("Single-node view: %s", report.FocusNode)))
	}
	if report.DoubleSignDetected {
		fmt.Fprintf(&b, "%s\n", c.red("⚠ Conflicting vote (equivocation) detected at this height"))
	}
	if !report.CommittedInLog && len(report.Rounds) > 0 {
		fmt.Fprintf(&b, "%s\n", c.yellow("⚠ block not committed in the available log window"))
	}

	// ── Clock synchronisation ────────────────────────────────────────────────
	if len(report.ClockSync) >= 2 {
		b.WriteString("\n" + c.bold("Clock synchronisation") + "\n")
		writeClockSync(&b, c, report.ClockSync)
	}

	// ── Consensus narrative ──────────────────────────────────────────────────
	if len(report.Rounds) > 0 {
		b.WriteString("\n" + c.bold("Consensus narrative") + "\n")
		if report.FocusNode != "" {
			fmt.Fprintf(&b, "%s\n", c.dim("(single-node view: "+report.FocusNode+")"))
		} else {
			fmt.Fprintf(&b, "%s\n", c.dim("(aggregate across all provided logs)"))
		}
		writeNarrativeTable(&b, c, report.Rounds)
	}

	// ── Validator vote detail ────────────────────────────────────────────────
	if len(report.ValidatorVotes) > 0 && hasAnyVoteData(report.ValidatorVotes) {
		b.WriteString("\n" + c.bold("Validator vote detail"))
		if report.FocusNode != "" {
			fmt.Fprintf(&b, " — %s\n", c.dim("single-node view: "+report.FocusNode))
		} else {
			fmt.Fprintf(&b, " — %s\n", c.dim("aggregate  (use --node <name> for single-node view)"))
		}
		if report.ValidatorSetSize > len(report.ValidatorVotes) {
			fmt.Fprintf(&b, "%s\n", c.yellow(fmt.Sprintf(
				"  ⚠ runtime validator set has %d members but genesis has %d — vote grid may be incomplete",
				report.ValidatorSetSize, len(report.ValidatorVotes))))
		}
		writeVoteGrid(&b, c, report.ValidatorVotes, report.Rounds)
	}

	// ── Peer connections ─────────────────────────────────────────────────────
	b.WriteString("\n" + c.bold("Peer connections during block"))
	if !report.PeerWindowStart.IsZero() || !report.PeerWindowEnd.IsZero() {
		fmt.Fprintf(&b, "  (%s → %s)",
			formatTimeShort(report.PeerWindowStart),
			formatTimeShort(report.PeerWindowEnd))
	}
	b.WriteString("\n")
	if len(report.PeerEvents) == 0 {
		fmt.Fprintf(&b, "%s\n", c.dim("  no peer connection changes observed in this window"))
	} else {
		writePeerEvents(&b, c, report.PeerEvents)
	}

	// ── Commit signatures ────────────────────────────────────────────────────
	if len(report.CommitSigs) > 0 {
		commitRound := -1
		for _, sig := range report.CommitSigs {
			if sig.Signed && sig.Round > commitRound {
				commitRound = sig.Round
			}
		}
		heading := "Commit signatures"
		if commitRound >= 0 {
			heading += fmt.Sprintf("  (round %d)", commitRound)
		}
		b.WriteString("\n" + c.bold(heading) + "  " + c.dim("[from RPC /commit]") + "\n")
		writeCommitSigs(&b, c, report.CommitSigs)
	}

	// ── Transactions ─────────────────────────────────────────────────────────
	if len(report.TxResults) > 0 {
		b.WriteString("\n" + c.bold("Transactions") + "  " + c.dim("[from RPC /block_results]") + "\n")
		writeTxResults(&b, c, report.TxResults)
	}

	// ── Warnings ─────────────────────────────────────────────────────────────
	for _, w := range report.Warnings {
		fmt.Fprintf(&b, "%s %s\n", c.yellow("warn:"), w)
	}

	return b.String()
}

// ── section renderers ────────────────────────────────────────────────────────

func writeClockSync(b *strings.Builder, c colorizer, rows []model.ClockSyncRow) {
	// Columns: Node | EnterPropose | FinalizeCommit | Δ median | Status
	const (
		colNode   = 16
		colTime   = 22
		colDelta  = 10
		colStatus = 10
	)
	header := fmt.Sprintf(" %-*s  %-*s  %-*s  %-*s  %s",
		colNode, "Node",
		colTime, "EnterPropose(H/0)",
		colTime, "FinalizeCommit(H)",
		colDelta, "Δ median",
		"Status")
	sep := strings.Repeat("─", len(header))
	fmt.Fprintf(b, " %s\n%s\n", header, sep)
	for _, row := range rows {
		ep := formatTimeShort(row.EnterProposeTime)
		fc := formatTimeShort(row.FinalizeCommitTime)
		delta := "—"
		if row.Status != "unknown" {
			if row.DeltaMs >= 0 {
				delta = fmt.Sprintf("+%dms", row.DeltaMs)
			} else {
				delta = fmt.Sprintf("%dms", row.DeltaMs)
			}
		}
		status := row.Status
		statusStr := status
		switch status {
		case "ok":
			statusStr = c.green("ok")
		case "warn":
			statusStr = c.yellow("warn")
		case "critical":
			statusStr = c.red("critical")
		}
		fmt.Fprintf(b, " %-*s  %-*s  %-*s  %-*s  %s\n",
			colNode, truncate(row.Node, colNode),
			colTime, ep,
			colTime, fc,
			colDelta, delta,
			statusStr)
	}
}

func writeNarrativeTable(b *strings.Builder, c colorizer, rounds []model.RoundSummary) {
	// Columns: Round | Proposal | Prevote outcome | Precommit outcome | Result
	type row struct {
		round      string
		proposal   string
		prevote    string
		precommit  string
		result     string
		resultColor func(string) string
	}

	rows := make([]row, len(rounds))
	for i, rs := range rounds {
		r := row{round: fmt.Sprintf("%d", rs.Round)}

		// Proposal cell
		if rs.ProposalSeen {
			if rs.ProposalHash != "" {
				r.proposal = fmt.Sprintf("Yes, block %s…", rs.ProposalHash)
			} else {
				r.proposal = "Yes (hash unknown)"
			}
			if rs.ProposalFromRound > 0 {
				r.proposal += fmt.Sprintf(" (from r%d)", rs.ProposalFromRound)
			}
			if !rs.ProposalValid {
				r.proposal += " [invalid]"
			}
			if rs.ProposalReceivedLate {
				if rs.ProposalLateTimeStr != "" {
					r.proposal += fmt.Sprintf(" [late@%s]", rs.ProposalLateTimeStr)
				} else {
					r.proposal += " [late]"
				}
			}
		} else if rs.TimedOut {
			r.proposal = "No proposal before timeout"
		} else {
			r.proposal = "Not observed"
		}
		if rs.ProposerAddr != "" {
			r.proposal += fmt.Sprintf(" (prop: %s)", shortAddrDisplay(rs.ProposerAddr))
		}

		r.prevote = rs.PrevoteNarrative
		r.precommit = rs.PrecommitNarrative

		if rs.Committed {
			r.result = "Committed"
			r.resultColor = c.green
		} else {
			r.result = "Failed"
			r.resultColor = c.red
		}
		rows[i] = r
	}

	// Compute column widths.
	wRound, wProposal, wPrevote, wPrecommit, wResult := 5, 8, 7, 10, 6
	for _, r := range rows {
		if l := len(r.round) + 2; l > wRound {
			wRound = l
		}
		if l := len(r.proposal) + 2; l > wProposal {
			wProposal = l
		}
		if l := len(r.prevote) + 2; l > wPrevote {
			wPrevote = l
		}
		if l := len(r.precommit) + 2; l > wPrecommit {
			wPrecommit = l
		}
		if l := len(r.result) + 2; l > wResult {
			wResult = l
		}
	}

	fmtRow := func(round, proposal, prevote, precommit, result string) string {
		return fmt.Sprintf(" %-*s │ %-*s │ %-*s │ %-*s │ %s",
			wRound, round,
			wProposal, proposal,
			wPrevote, prevote,
			wPrecommit, precommit,
			result)
	}
	header := fmtRow("Round", "Proposal", "Prevote", "Precommit", "Result")
	sep := strings.Repeat("─", len(header)+4)
	fmt.Fprintf(b, " %s\n%s\n", header, sep)

	for _, r := range rows {
		result := r.result
		if r.resultColor != nil {
			result = r.resultColor(r.result)
		}
		fmt.Fprintf(b, "%s\n", fmtRow(r.round, r.proposal, r.prevote, r.precommit, result))
	}
}

func writeVoteGrid(b *strings.Builder, c colorizer, rows []model.ValidatorVoteRow, rounds []model.RoundSummary) {
	if len(rounds) == 0 || len(rows) == 0 {
		return
	}

	// Collect round numbers in order.
	roundNums := make([]int, len(rounds))
	for i, rs := range rounds {
		roundNums[i] = rs.Round
	}

	const colVal = 28
	// Build header: Validator | R0 pv | R0 pc | R1 pv | …
	var hdr strings.Builder
	fmt.Fprintf(&hdr, " %-*s", colVal, "Validator [idx]")
	for _, r := range roundNums {
		fmt.Fprintf(&hdr, " │ R%d pv  │ R%d pc  ", r, r)
	}
	header := hdr.String()
	sep := strings.Repeat("─", len(header)+2)
	fmt.Fprintf(b, " %s\n%s\n", header, sep)

	hasLate := false
	for _, vr := range rows {
		addrSuffix := ""
		if len(vr.Addr) >= 6 {
			addrSuffix = "(" + vr.Addr[:6] + ")"
		}
		label := fmt.Sprintf("%s%s [%d]", truncate(vr.Name, 14), addrSuffix, vr.Index)
		var line strings.Builder
		fmt.Fprintf(&line, " %-*s", colVal, label)
		for _, r := range roundNums {
			entry, ok := vr.ByRound[r]
			pv := "—"
			pc := "—"
			if ok {
				pv = colorVote(c, entry.Prevote)
				pc = colorVote(c, entry.Precommit)
				if entry.Prevote == model.VoteLateBlock || entry.Prevote == model.VoteLateNil ||
					entry.Precommit == model.VoteLateBlock || entry.Precommit == model.VoteLateNil {
					hasLate = true
				}
			}
			fmt.Fprintf(&line, " │ %-6s │ %-6s ", pv, pc)
		}
		fmt.Fprintf(b, "%s\n", line.String())
	}
	if hasLate {
		fmt.Fprintf(b, "%s\n", c.dim("  † = vote arrived after step timeout"))
	}
}

func colorVote(c colorizer, k model.VoteKind) string {
	switch k {
	case model.VoteBlock:
		return c.green("block")
	case model.VoteLateBlock:
		return c.yellow("block†")
	case model.VoteNil:
		return c.dim("nil")
	case model.VoteLateNil:
		return c.yellow("nil†")
	case model.VoteOtherBlock:
		return c.red("other")
	case model.VoteAbsent:
		return c.red("absent")
	}
	return string(k)
}

func writePeerEvents(b *strings.Builder, c colorizer, events []model.PeerEvent) {
	// Group by node, preserving event order within each node.
	type nodeGroup struct {
		node   string
		events []model.PeerEvent
	}
	seen := map[string]int{}
	var groups []nodeGroup
	for _, ev := range events {
		if _, ok := seen[ev.Node]; !ok {
			seen[ev.Node] = len(groups)
			groups = append(groups, nodeGroup{node: ev.Node})
		}
		i := seen[ev.Node]
		groups[i].events = append(groups[i].events, ev)
	}

	for _, g := range groups {
		fmt.Fprintf(b, " %s\n", c.bold(g.node))
		for _, ev := range g.events {
			sign := c.green("+")
			verb := "added"
			if !ev.Added {
				sign = c.red("−")
				verb = "dropped"
			}
			peer := ev.PeerAddr
			if ev.PeerName != "" {
				peer = fmt.Sprintf("%s (%s)", ev.PeerName, shortAddrDisplay(ev.PeerAddr))
			}
			ts := ev.Timestamp.UTC().Format("15:04:05.000Z")
			line := fmt.Sprintf("   %s %s  %s  %s", sign, verb, peer, c.dim(ts))
			if ev.ErrReason != "" {
				line += c.dim(" — " + ev.ErrReason)
			}
			fmt.Fprintln(b, line)
		}
	}
}

func writeCommitSigs(b *strings.Builder, c colorizer, sigs []model.CommitSig) {
	for _, sig := range sigs {
		name := sig.ValidatorName
		if name == "" {
			name = shortAddrDisplay(sig.ValidatorAddr)
		}
		label := fmt.Sprintf("%s [%d]", name, sig.Index)
		if sig.Signed {
			fmt.Fprintf(b, "  %s  %-32s  %s\n", c.green("yes"), label, c.dim(shortAddrDisplay(sig.ValidatorAddr)))
		} else {
			fmt.Fprintf(b, "  %s   %-32s  %s\n", c.red("no"), label, c.dim(shortAddrDisplay(sig.ValidatorAddr)))
		}
	}
}

func writeTxResults(b *strings.Builder, c colorizer, txs []model.TxSummary) {
	fmt.Fprintf(b, " %-4s  %-12s  %-12s  %s\n", "Tx #", "Gas wanted", "Gas used", "Error")
	sep := strings.Repeat("─", 60)
	fmt.Fprintln(b, " "+sep)
	for i, tx := range txs {
		errStr := c.dim("—")
		if tx.Error != "" {
			errStr = c.red(tx.Error)
		}
		fmt.Fprintf(b, " %-4d  %-12d  %-12d  %s\n", i, tx.GasWanted, tx.GasUsed, errStr)
	}
}

// ── display helpers ───────────────────────────────────────────────────────────

func hasAnyVoteData(rows []model.ValidatorVoteRow) bool {
	for _, r := range rows {
		if len(r.ByRound) > 0 {
			return true
		}
	}
	return false
}

func formatTimeShort(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("15:04:05.000Z")
}

func shortAddrDisplay(addr string) string {
	if len(addr) <= 12 {
		return addr
	}
	return addr[:8] + "…"
}

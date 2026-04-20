package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderHelp(m Model) string {
	title := m.styles.title.Render("Valdoctor — Help")
	meta := m.styles.muted.Render("Scrollable reference. Use ↑/↓ or j/k to move, PgUp/PgDn to page, h or Esc to close.")
	body := m.viewport.View()
	help := m.styles.help.Render(keyHelp(m.mode, m.searching, m.showHelp, m.confirmQuit))

	return m.styles.doc.Render(strings.Join([]string{
		title,
		meta,
		body,
		help,
	}, "\n\n"))
}

func helpContent(m Model) string {
	var b strings.Builder

	section := func(title string) {
		b.WriteString(m.styles.sectionTitle.Render(title) + "\n")
	}
	row := func(key, desc string) {
		b.WriteString(m.styles.tableHeader.Render("  "+key) + "  " + desc + "\n")
	}
	note := func(text string) {
		b.WriteString(m.styles.muted.Render("  "+text) + "\n")
	}
	nl := func() { b.WriteString("\n") }

	// ── Keyboard ─────────────────────────────────────────────────────────────
	section("Keyboard shortcuts")
	nl()
	row("h", "toggle this help window")
	row("q / ctrl+c", "open quit confirmation")
	row("Space", "pause live updates (press again to resume)")
	row("/ + Enter", "search incidents by keyword")
	row("f", "cycle severity filter (all → crit → high → med → low → info)")
	row("↑/↓ or j/k", "navigate incident list")
	row("Enter", "open detail view for selected incident")
	nl()
	note("In detail view:")
	row("Tab", "switch between Consensus and Propagation tabs")
	row("n / p", "go to next / previous height in the retained window")
	row("↑/↓ or j/k", "scroll content")
	row("b / Esc", "return to dashboard")
	nl()

	// ── Dashboard — Nodes ────────────────────────────────────────────────────
	section("Dashboard — Nodes table")
	nl()
	row("Node", "validator name followed by the first 12 chars of its bech32 address")
	row("Role", "role inferred from config: validator / sentry / seed")
	row("Commit", "highest block height committed in the log window (hN)")
	row("Peers (cur/max)", "current active P2P connections / maximum seen during the window")
	note("0/0 means no peer-add/drop logs were seen in the observed window.")
	note("If valdoctor started after the cluster formed, debug peer gossip may backfill this count.")
	row("Round (max@h)", "highest consensus round reached at any single height")
	note("This is a stall indicator, not a per-block live round ticker.")
	note("0@0 is normal — a healthy chain keeps committing at round 0.")
	note("e.g. 2@4500 means round 2 was needed to commit height 4500 (stall indicator).")
	row("Signer (fail/conn)", "remote signer failure count / reconnection count")
	note("This only changes on signer failures/reconnects; steady successful signing may stay flat.")
	note("0/0 is normal for a directly signing validator with no remote signer.")
	nl()

	// ── Dashboard — Incidents ────────────────────────────────────────────────
	section("Dashboard — Incidents")
	nl()
	row("Severity", "CRIT  HIGH  MED  LOW  INFO  (most → least urgent)")
	row("Status", "ACTIVE = ongoing  ·  resolved = cleared in a recent height")
	row("Scope", "which node or validator set the incident concerns")
	row("h N→M", "first and last height at which the incident was observed")
	nl()

	// ── Detail view — Consensus tab ──────────────────────────────────────────
	section("Detail — Consensus tab")
	nl()
	note("Shows what happened at a specific height, one column per consensus round.")
	nl()
	row("Validator vote grid", "one row per validator slot (genesis or active valset order)")
	row("  pv", "prevote outcome for this round")
	row("  pc", "precommit outcome for this round")
	nl()
	note("Vote kinds:")
	row("  block", "voted for the proposal block")
	row("  nil", "voted nil (validator timed out before receiving a valid proposal)")
	row("  other", "voted for a different block hash (byzantine or fork)")
	row("  absent", "no vote observed in any VoteSet BitArray before the block committed")
	row("  —", "no vote data at all for this round/validator combination")
	row("  †late", "vote arrived after the prevote step timeout")
	nl()
	note("'absent' in the vote grid does NOT mean the validator definitely did not vote.")
	note("It means the vote was not counted in the 2/3+ quorum that committed the block.")
	note("A validator can show 'absent' here yet 'ok' in propagation if its vote arrived")
	note("at peers after the block was already committed.")
	nl()
	row("Prevote narrative", "summarises what 2/3+ prevoted for at this round")
	row("Precommit narrative", "summarises what 2/3+ precommitted; '+2/3 block' = committed")
	nl()

	// ── Detail view — Propagation tab ────────────────────────────────────────
	section("Detail — Propagation tab")
	nl()
	note("Tracks how each signed vote traveled from its origin to every other node.")
	note("Rows = votes (origin validator). Columns = receiving validators.")
	nl()
	row("ok Xs", "vote received Xs after it was cast — within the 500ms threshold")
	row("late Xs", "vote received but latency > 500ms")
	row("missing", "receiver logs vote receipts for this height but never got this one")
	note("'missing' is only shown when the receiver has other receipt log entries,")
	note("meaning it does log receipts and this gap is real, not a logging artifact.")
	row("?", "cast time unknown (no EventSignedVote seen), latency cannot be computed")
	row("-", "receiver has no vote receipt logs at all (INFO log level — normal)")
	row("pending", "height still active; data may update as more votes arrive")
	nl()
	note("'missing' in propagation + 'absent' in the vote grid: the vote was not")
	note("delivered to this peer at all — a genuine network connectivity issue.")
	note("'ok' in propagation + 'absent' in the vote grid: the vote was delivered")
	note("but arrived after the block already committed (was not part of the quorum).")
	nl()

	return strings.TrimRight(b.String(), "\n")
}

func renderQuitConfirm(m Model) string {
	title := m.styles.sectionTitle.Render("Quit Valdoctor?")
	body := m.styles.muted.Render("Enter confirms the selected option. Press Ctrl+C again to quit immediately.")

	noStyle := m.styles.modalActive
	yesStyle := m.styles.modalChoice
	if m.quitYes {
		yesStyle = m.styles.modalActive
		noStyle = m.styles.modalChoice
	}

	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		noStyle.Render("No"),
		" ",
		yesStyle.Render("Yes"),
	)
	dialog := m.styles.modal.Render(strings.Join([]string{title, "", body, "", buttons}, "\n"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

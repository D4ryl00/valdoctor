package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
)

func renderDashboard(m Model) string {
	var sections []string

	header := m.styles.title.Render("Valdoctor Live")
	status := m.styles.muted.Render(m.statusLine())
	sections = append(sections, header+"\n"+status)

	if m.err != nil {
		sections = append(sections, m.styles.error.Render("Coordinator error: "+m.err.Error()))
	}
	if m.searching {
		sections = append(sections, m.styles.searchBox.Render(m.searchInput.View()))
	}

	sections = append(sections, renderNodeSection(m))
	sections = append(sections, renderIncidentSection(m, "Active Incidents", "active"))
	sections = append(sections, renderIncidentSection(m, "Recent Resolved", "resolved"))
	sections = append(sections, m.styles.help.Render(keyHelp(m.mode, m.searching)))

	return m.styles.doc.Render(strings.Join(sections, "\n\n"))
}

func renderNodeSection(m Model) string {
	var b strings.Builder
	b.WriteString(m.styles.sectionTitle.Render("Nodes"))
	b.WriteString("\n")

	if len(m.snap.nodes) == 0 {
		b.WriteString(m.styles.muted.Render("No node state yet. Waiting for classified live events."))
		return b.String()
	}

	header := fmt.Sprintf("%-18s %-10s %-8s %-9s %-10s %-10s", "Node", "Role", "Commit", "Peers", "Round", "Signer")
	b.WriteString(m.styles.tableHeader.Render(header))
	for _, node := range m.snap.nodes {
		summary := node.Summary
		line := fmt.Sprintf(
			"%-18s %-10s %-8s %-9s %-10s %-10s",
			truncate(summary.Name, 18),
			string(summary.Role),
			fmt.Sprintf("h%d", summary.HighestCommit),
			fmt.Sprintf("%d/%d", summary.CurrentPeers, summary.MaxPeers),
			fmt.Sprintf("%d@%d", summary.MaxRoundSeen, summary.MaxRoundHeight),
			fmt.Sprintf("%d/%d", summary.SignerFailureCount, summary.SignerConnectCount),
		)
		b.WriteString("\n" + line)
		if summary.StallDuration > 0 && summary.HighestCommit < m.snap.tip {
			b.WriteString("\n" + m.styles.muted.Render(fmt.Sprintf("  stall %s since %s", summary.StallDuration.Round(time.Second), formatMaybeTime(summary.LastCommitTime))))
		}
	}
	return b.String()
}

func renderIncidentSection(m Model, title, status string) string {
	var b strings.Builder
	b.WriteString(m.styles.sectionTitle.Render(title))
	b.WriteString("\n")

	visible := m.visibleIncidents()
	found := false
	for index, item := range visible {
		if item.status == status {
			found = true
			selected := index == m.incidentSelection
			b.WriteString(renderIncidentRow(m, item.card, item.status, selected))
			b.WriteString("\n")
		}
	}
	if !found {
		b.WriteString(m.styles.muted.Render("No incidents match the current filter."))
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderIncidentRow(m Model, card model.IncidentCard, status string, selected bool) string {
	prefix := "  "
	if selected {
		prefix = "› "
	}

	statusStyle := m.styles.activeChip
	if status == "resolved" {
		statusStyle = m.styles.resolvedChip
	}

	severityStyle := m.styles.severityStyle(card.Severity)
	line := fmt.Sprintf(
		"%s%s %s %s [%s] h%d→h%d",
		prefix,
		statusStyle.Render(strings.ToUpper(status[:1])),
		severityStyle.Render(strings.ToUpper(string(card.Severity))),
		card.Title,
		card.Scope,
		card.FirstHeight,
		card.LastHeight,
	)
	summary := "   " + m.styles.muted.Render(card.Summary)

	if selected {
		return m.styles.selected.Render(line) + "\n" + m.styles.selected.Render(summary)
	}
	return line + "\n" + summary
}

func formatMaybeTime(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

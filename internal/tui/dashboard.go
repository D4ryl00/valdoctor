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

	activeLimit, resolvedLimit := dashboardIncidentBudgets(m)
	sections = append(sections, renderNodeSection(m))
	sections = append(sections, renderIncidentSection(m, "Active Incidents", "active", activeLimit))
	sections = append(sections, renderIncidentSection(m, "Recent Resolved", "resolved", resolvedLimit))
	sections = append(sections, m.styles.help.Render(keyHelp(m.mode, m.searching, m.showHelp, m.confirmQuit)))

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

	header := fmt.Sprintf("%-30s %-10s %-8s %-9s %-10s %-10s", "Node", "Role", "Commit", "Peers", "Round", "Signer")
	b.WriteString(m.styles.tableHeader.Render(header))
	for _, node := range m.snap.nodes {
		summary := node.Summary
		nameField := truncate(summary.Name, 18)
		if summary.ShortAddr != "" {
			nameField = truncate(summary.Name, 16) + " " + truncate(summary.ShortAddr, 12)
		}
		line := fmt.Sprintf(
			"%-30s %-10s %-8s %-9s %-10s %-10s",
			nameField,
			string(summary.Role),
			fmt.Sprintf("h%d", summary.HighestCommit),
			fmt.Sprintf("%d/%d", summary.CurrentPeers, summary.MaxPeers),
			fmt.Sprintf("%d@%d", summary.MaxRoundSeen, summary.MaxRoundHeight),
			fmt.Sprintf("%d/%d", summary.SignerFailureCount, summary.SignerConnectCount),
		)
		b.WriteString("\n" + line)
		if summary.StallDuration >= summary.StallThreshold() && summary.HighestCommit < m.snap.tip {
			b.WriteString("\n" + m.styles.muted.Render(fmt.Sprintf("  stall %s since %s", summary.StallDuration.Round(time.Second), formatMaybeTime(summary.LastCommitTime))))
		}
	}
	return b.String()
}

func renderIncidentSection(m Model, title, status string, limit int) string {
	var b strings.Builder
	b.WriteString(m.styles.sectionTitle.Render(title))
	b.WriteString("\n")

	items := incidentsByStatus(m.visibleIncidents(), status)
	if len(items) == 0 {
		b.WriteString(m.styles.muted.Render("No incidents match the current filter."))
		return strings.TrimRight(b.String(), "\n")
	}

	start, end := incidentWindow(items, selectedIncidentIndex(items, m.selectedIncidentItem()), limit)
	if start > 0 {
		b.WriteString(m.styles.muted.Render(fmt.Sprintf("… %d earlier incidents", start)))
		b.WriteString("\n")
	}

	for _, item := range items[start:end] {
		selected := selectedIncidentMatches(item, m.selectedIncidentItem())
		b.WriteString(renderIncidentRow(m, item.card, item.status, selected))
		b.WriteString("\n")
	}
	if end < len(items) {
		b.WriteString(m.styles.muted.Render(fmt.Sprintf("… %d more incidents", len(items)-end)))
	}

	return strings.TrimRight(b.String(), "\n")
}

func dashboardIncidentBudgets(m Model) (int, int) {
	if m.height <= 0 {
		return 4, 2
	}

	headerLines := 2
	nodeLines := countLines(renderNodeSection(m))
	searchLines := 0
	if m.searching {
		searchLines = countLines(m.searchInput.View())
	}
	errorLines := 0
	if m.err != nil {
		errorLines = 1
	}
	helpLines := 1
	baseLines := headerLines + nodeLines + searchLines + errorLines + helpLines + 8
	available := m.height - baseLines
	if available < 4 {
		available = 4
	}

	totalItems := available / 2
	if totalItems < 1 {
		totalItems = 1
	}

	resolvedItems := len(incidentsByStatus(m.visibleIncidents(), "resolved"))
	if resolvedItems == 0 {
		return totalItems, 0
	}
	if totalItems == 1 {
		return 1, 0
	}

	resolvedLimit := min(resolvedItems, maxInt(1, totalItems/3))
	activeLimit := totalItems - resolvedLimit
	if activeLimit < 1 {
		activeLimit = 1
		if resolvedLimit > 1 {
			resolvedLimit--
		}
	}

	return activeLimit, resolvedLimit
}

func incidentsByStatus(items []incidentItem, status string) []incidentItem {
	filtered := make([]incidentItem, 0, len(items))
	for _, item := range items {
		if item.status == status {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func selectedIncidentIndex(items []incidentItem, selected incidentItem) int {
	for i, item := range items {
		if selectedIncidentMatches(item, selected) {
			return i
		}
	}
	return 0
}

func selectedIncidentMatches(item, selected incidentItem) bool {
	return item.status == selected.status && item.card.ID == selected.card.ID && item.card.ID != ""
}

func (m Model) selectedIncidentItem() incidentItem {
	visible := m.visibleIncidents()
	if len(visible) == 0 || m.incidentSelection < 0 || m.incidentSelection >= len(visible) {
		return incidentItem{}
	}
	return visible[m.incidentSelection]
}

func incidentWindow(items []incidentItem, selectedIdx, limit int) (int, int) {
	if limit <= 0 || len(items) <= limit {
		return 0, len(items)
	}
	if selectedIdx < 0 {
		selectedIdx = 0
	}
	start := selectedIdx - limit/2
	if start < 0 {
		start = 0
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
		start = end - limit
	}
	return start, end
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func formatMaybeTime(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type styles struct {
	doc          lipgloss.Style
	title        lipgloss.Style
	muted        lipgloss.Style
	sectionTitle lipgloss.Style
	tableHeader  lipgloss.Style
	selected     lipgloss.Style
	activeChip   lipgloss.Style
	resolvedChip lipgloss.Style
	activeTab    lipgloss.Style
	inactiveTab  lipgloss.Style
	searchBox    lipgloss.Style
	help         lipgloss.Style
	error        lipgloss.Style
}

func newStyles(color bool) styles {
	base := lipgloss.NewStyle().Padding(0, 1)
	if !color {
		return styles{
			doc:          lipgloss.NewStyle().Padding(1, 2),
			title:        lipgloss.NewStyle().Bold(true),
			muted:        lipgloss.NewStyle().Faint(true),
			sectionTitle: lipgloss.NewStyle().Bold(true),
			tableHeader:  lipgloss.NewStyle().Bold(true),
			selected:     lipgloss.NewStyle().Bold(true),
			activeChip:   base.Bold(true),
			resolvedChip: base.Bold(true),
			activeTab:    base.Bold(true).Underline(true),
			inactiveTab:  base.Faint(true),
			searchBox:    lipgloss.NewStyle().Padding(0, 1).Border(lipgloss.NormalBorder()),
			help:         lipgloss.NewStyle().Faint(true),
			error:        lipgloss.NewStyle().Bold(true),
		}
	}

	return styles{
		doc:          lipgloss.NewStyle().Padding(1, 2),
		title:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")).Padding(0, 1),
		muted:        lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		sectionTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("74")),
		tableHeader:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")),
		selected:     lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("230")),
		activeChip:   base.Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("160")),
		resolvedChip: base.Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("241")),
		activeTab:    base.Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")),
		inactiveTab:  base.Foreground(lipgloss.Color("245")).Background(lipgloss.Color("236")),
		searchBox:    lipgloss.NewStyle().Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("67")),
		help:         lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		error:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("160")).Padding(0, 1),
	}
}

func (s styles) severityStyle(severity model.Severity) lipgloss.Style {
	switch severity {
	case model.SeverityCritical:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	case model.SeverityHigh:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("215"))
	case model.SeverityMedium:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("186"))
	case model.SeverityLow:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("150"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	}
}

func truncate(value string, width int) string {
	if width <= 0 || len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

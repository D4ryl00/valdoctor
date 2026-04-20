package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/D4ryl00/valdoctor/internal/live"
	"github.com/D4ryl00/valdoctor/internal/model"
	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

type viewMode int

const (
	viewDashboard viewMode = iota
	viewDetail
)

type detailTab int

const (
	tabConsensus detailTab = iota
	tabPropagation
)

type storeUpdatedMsg struct{}
type storeEventMsg struct {
	event storepkg.StoreEvent
}

type coordinatorErrMsg struct {
	err error
}

type Options struct {
	Store       storepkg.Store
	Coordinator *live.Coordinator
	ChainID     string
	Color       bool
}

type snapshot struct {
	tip               int64
	nodes             []model.NodeState
	activeIncidents   []model.IncidentCard
	resolvedIncidents []model.IncidentCard
	recentHeights     []model.HeightEntry
}

type incidentItem struct {
	card   model.IncidentCard
	status string
}

type Model struct {
	store   storepkg.Store
	chainID string
	color   bool
	styles  styles

	width  int
	height int

	mode      viewMode
	detailTab detailTab

	paused      bool
	dirty       bool
	searching   bool
	showHelp    bool
	confirmQuit bool
	quitYes     bool

	searchQuery    string
	searchInput    textinput.Model
	severityFilter model.Severity

	incidentSelection int
	selectedHeight    int64
	followLatest      bool

	viewport viewport.Model
	snap     snapshot
	err      error
}

func Run(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	subscription, unsubscribe := opts.Store.Subscribe()
	defer unsubscribe()

	program := tea.NewProgram(newModel(opts), tea.WithAltScreen())

	coordDone := make(chan error, 1)
	coordNotify := make(chan error, 1)
	go func() {
		err := opts.Coordinator.Run(ctx)
		coordDone <- err
		coordNotify <- err
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-subscription:
				if !ok {
					return
				}
				program.Send(storeEventMsg{event: event})
			}
		}
	}()

	go func() {
		err := <-coordNotify
		if err != nil && !errors.Is(err, context.Canceled) {
			program.Send(coordinatorErrMsg{err: err})
		}
	}()

	if _, err := program.Run(); err != nil {
		cancel()
		<-coordDone
		return err
	}

	cancel()
	if err := <-coordDone; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

func newModel(opts Options) Model {
	search := textinput.New()
	search.Placeholder = "incident search"
	search.CharLimit = 120
	search.Prompt = "/ "

	m := Model{
		store:       opts.Store,
		chainID:     opts.ChainID,
		color:       opts.Color,
		styles:      newStyles(opts.Color),
		mode:        viewDashboard,
		detailTab:   tabConsensus,
		searchInput: search,
		viewport:    viewport.New(0, 0),
	}
	m.reloadSnapshot()
	return m
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	case storeUpdatedMsg:
		if m.paused {
			m.dirty = true
			return m, nil
		}
		m.reloadSnapshot()
		return m, nil
	case storeEventMsg:
		if m.paused {
			m.dirty = true
			return m, nil
		}
		m.applyStoreEvent(msg.event)
		return m, nil
	case coordinatorErrMsg:
		m.err = msg.err
		return m, nil
	case tea.KeyMsg:
		if m.searching {
			return m.handleSearchKey(msg)
		}
		return m.handleKey(msg)
	}

	if m.mode == viewDetail || m.showHelp {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading..."
	}

	var view string
	if m.showHelp {
		view = renderHelp(m)
	} else if m.mode == viewDetail {
		view = renderDetail(m)
	} else {
		view = renderDashboard(m)
	}
	if m.confirmQuit {
		return renderQuitConfirm(m)
	}
	return view
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.confirmQuit {
			return *m, tea.Quit
		}
		m.confirmQuit = true
		m.quitYes = false
		return *m, nil
	case "esc":
		m.searching = false
		m.searchInput.Blur()
		return *m, nil
	case "enter":
		m.searchQuery = strings.TrimSpace(m.searchInput.Value())
		m.searching = false
		m.searchInput.Blur()
		m.incidentSelection = 0
		return *m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return *m, cmd
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmQuit {
		switch msg.String() {
		case "ctrl+c":
			return *m, tea.Quit
		case "left", "h", "shift+tab":
			m.quitYes = false
		case "right", "l", "tab":
			m.quitYes = true
		case "y", "Y":
			m.quitYes = true
			return *m, tea.Quit
		case "n", "N", "esc", "q":
			m.confirmQuit = false
			m.quitYes = false
		case "enter", " ":
			if m.quitYes {
				return *m, tea.Quit
			}
			m.confirmQuit = false
			m.quitYes = false
		}
		return *m, nil
	}

	if m.showHelp {
		return m.handleHelpKey(msg)
	}

	switch msg.String() {
	case "ctrl+c", "q":
		m.confirmQuit = true
		m.quitYes = false
		return *m, nil
	case "h":
		m.showHelp = !m.showHelp
		m.refreshViewport()
		return *m, nil
	case " ":
		m.paused = !m.paused
		if !m.paused {
			m.reloadSnapshot()
			m.dirty = false
		}
		return *m, nil
	case "/":
		m.searching = true
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.Focus()
		return *m, textinput.Blink
	case "f":
		m.cycleSeverityFilter()
		m.incidentSelection = 0
		return *m, nil
	}

	switch m.mode {
	case viewDashboard:
		return m.handleDashboardKey(msg)
	case viewDetail:
		return m.handleDetailKey(msg)
	default:
		return *m, nil
	}
}

func (m *Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.confirmQuit = true
		m.quitYes = false
	case "h", "esc":
		m.showHelp = false
		m.refreshViewport()
	case "j", "down":
		m.viewport.LineDown(1)
	case "k", "up":
		m.viewport.LineUp(1)
	case "pgdown":
		m.viewport.ViewDown()
	case "pgup":
		m.viewport.ViewUp()
	case "home":
		m.viewport.GotoTop()
	case "end":
		m.viewport.GotoBottom()
	}

	return *m, nil
}

func (m *Model) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		items := m.visibleIncidents()
		if len(items) > 0 && m.incidentSelection < len(items)-1 {
			m.incidentSelection++
		}
	case "k", "up":
		if m.incidentSelection > 0 {
			m.incidentSelection--
		}
	case "enter":
		items := m.visibleIncidents()
		if len(items) > 0 {
			m.mode = viewDetail
			m.detailTab = tabConsensus
			m.selectedHeight = items[m.incidentSelection].card.LastHeight
			m.followLatest = false
			m.refreshViewport()
		} else if len(m.snap.recentHeights) > 0 {
			m.mode = viewDetail
			m.detailTab = tabConsensus
			m.selectedHeight = m.snap.recentHeights[0].Height
			m.followLatest = true
			m.refreshViewport()
		}
	}

	return *m, nil
}

func (m *Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "b":
		m.mode = viewDashboard
		m.followLatest = false
		return *m, nil
	case "tab":
		if m.detailTab == tabConsensus {
			m.detailTab = tabPropagation
		} else {
			m.detailTab = tabConsensus
		}
		m.refreshViewport()
	case "n":
		m.navigateHeight(-1)
	case "p":
		m.navigateHeight(1)
	case "N":
		m.jumpToLatestHeight()
	case "P":
		m.jumpToOldestHeight()
	case "j", "down":
		m.viewport.LineDown(1)
	case "k", "up":
		m.viewport.LineUp(1)
	}

	return *m, nil
}

func (m *Model) reloadSnapshot() {
	m.snap = snapshot{
		tip:               m.store.CurrentTip(),
		nodes:             m.store.NodeStates(),
		activeIncidents:   m.store.ActiveIncidents(),
		resolvedIncidents: m.store.RecentResolved(8),
		recentHeights:     m.store.RecentHeights(32),
	}

	if (m.selectedHeight == 0 || m.followLatest) && len(m.snap.recentHeights) > 0 {
		m.selectedHeight = m.snap.recentHeights[0].Height
	}
	if m.selectedHeight != 0 {
		if _, ok := m.store.GetHeight(m.selectedHeight); !ok && len(m.snap.recentHeights) > 0 {
			m.selectedHeight = m.snap.recentHeights[0].Height
		}
	}

	m.reconcileIncidentSelection()

	if !m.showHelp {
		m.refreshViewport()
	}
}

func (m *Model) applyStoreEvent(event storepkg.StoreEvent) {
	refreshViewport := false

	switch event.Kind {
	case "height_updated":
		m.snap.tip = m.store.CurrentTip()
		m.snap.recentHeights = m.store.RecentHeights(32)

		if len(m.snap.recentHeights) > 0 {
			switch {
			case m.selectedHeight == 0 || m.followLatest:
				next := m.snap.recentHeights[0].Height
				if next != m.selectedHeight {
					m.selectedHeight = next
					refreshViewport = true
				}
			case event.Height == m.selectedHeight:
				refreshViewport = true
			default:
				if _, ok := m.store.GetHeight(m.selectedHeight); !ok {
					m.selectedHeight = m.snap.recentHeights[0].Height
					refreshViewport = true
				}
			}
		}
	case "node_updated":
		m.snap.nodes = m.store.NodeStates()
		if m.mode == viewDetail && m.detailTab == tabPropagation {
			refreshViewport = true
		}
	case "incident_updated":
		m.snap.activeIncidents = m.store.ActiveIncidents()
		m.snap.resolvedIncidents = m.store.RecentResolved(8)
	}

	m.reconcileIncidentSelection()

	if m.showHelp {
		return
	}
	if m.mode == viewDetail && refreshViewport {
		m.refreshViewport()
	}
}

func (m *Model) resizeViewport() {
	contentWidth := m.width - 4
	contentHeight := m.height - 8
	if contentWidth < 20 {
		contentWidth = 20
	}
	if contentHeight < 5 {
		contentHeight = 5
	}
	m.viewport.Width = contentWidth
	m.viewport.Height = contentHeight
}

func (m *Model) refreshViewport() {
	if m.viewport.Width == 0 || m.viewport.Height == 0 {
		return
	}

	if m.showHelp {
		m.viewport.SetContent(helpContent(*m))
		m.viewport.GotoTop()
		return
	}
	if m.mode != viewDetail {
		return
	}

	entry, ok := m.currentHeightEntry()
	if !ok {
		m.viewport.SetContent("No height data available in the retained live window.")
		m.viewport.GotoTop()
		return
	}

	switch m.detailTab {
	case tabConsensus:
		m.viewport.SetContent(renderConsensusContent(entry, m.color))
	case tabPropagation:
		m.viewport.SetContent(renderPropagationContent(entry, m.snap.nodes))
	}
	m.viewport.GotoTop()
}

func (m Model) currentHeightEntry() (model.HeightEntry, bool) {
	if m.selectedHeight == 0 {
		return model.HeightEntry{}, false
	}
	return m.store.GetHeight(m.selectedHeight)
}

func (m Model) visibleIncidents() []incidentItem {
	items := make([]incidentItem, 0, len(m.snap.activeIncidents)+len(m.snap.resolvedIncidents))
	for _, card := range m.snap.activeIncidents {
		if m.matchesIncident(card) {
			items = append(items, incidentItem{card: card, status: "active"})
		}
	}
	for _, card := range m.snap.resolvedIncidents {
		if m.matchesIncident(card) {
			items = append(items, incidentItem{card: card, status: "resolved"})
		}
	}
	return items
}

func (m Model) matchesIncident(card model.IncidentCard) bool {
	if m.severityFilter != "" && card.Severity != m.severityFilter {
		return false
	}
	if m.searchQuery == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		card.ID,
		card.Kind,
		card.Scope,
		card.Title,
		card.Summary,
	}, " "))
	return strings.Contains(haystack, strings.ToLower(m.searchQuery))
}

func (m *Model) navigateHeight(delta int) {
	if len(m.snap.recentHeights) == 0 {
		return
	}
	m.followLatest = false

	index := 0
	for i, entry := range m.snap.recentHeights {
		if entry.Height == m.selectedHeight {
			index = i
			break
		}
	}

	next := index + delta
	if next < 0 || next >= len(m.snap.recentHeights) {
		return
	}
	m.selectedHeight = m.snap.recentHeights[next].Height
	m.refreshViewport()
}

func (m *Model) jumpToLatestHeight() {
	if len(m.snap.recentHeights) == 0 {
		return
	}
	m.selectedHeight = m.snap.recentHeights[0].Height
	m.followLatest = true
	m.refreshViewport()
}

func (m *Model) jumpToOldestHeight() {
	if len(m.snap.recentHeights) == 0 {
		return
	}
	m.selectedHeight = m.snap.recentHeights[len(m.snap.recentHeights)-1].Height
	m.followLatest = false
	m.refreshViewport()
}

func (m *Model) cycleSeverityFilter() {
	switch m.severityFilter {
	case "":
		m.severityFilter = model.SeverityCritical
	case model.SeverityCritical:
		m.severityFilter = model.SeverityHigh
	case model.SeverityHigh:
		m.severityFilter = model.SeverityMedium
	case model.SeverityMedium:
		m.severityFilter = model.SeverityLow
	case model.SeverityLow:
		m.severityFilter = model.SeverityInfo
	default:
		m.severityFilter = ""
	}
}

func (m *Model) reconcileIncidentSelection() {
	items := m.visibleIncidents()
	if len(items) == 0 {
		m.incidentSelection = 0
	} else if m.incidentSelection >= len(items) {
		m.incidentSelection = len(items) - 1
	}
}

func (m Model) statusLine() string {
	status := "live"
	if m.paused {
		status = "paused"
	}
	filter := "all"
	if m.severityFilter != "" {
		filter = string(m.severityFilter)
	}

	parts := []string{
		fmt.Sprintf("chain %s", m.chainID),
		fmt.Sprintf("tip h%d", m.snap.tip),
		fmt.Sprintf("%d nodes", len(m.snap.nodes)),
		fmt.Sprintf("%d active incidents", len(m.snap.activeIncidents)),
		fmt.Sprintf("filter %s", filter),
		status,
	}
	if m.searchQuery != "" {
		parts = append(parts, fmt.Sprintf("search %q", m.searchQuery))
	}
	if m.paused && m.dirty {
		parts = append(parts, "updates pending")
	}
	return strings.Join(parts, "  ·  ")
}

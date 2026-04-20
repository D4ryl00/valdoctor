package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/store"
)

func TestModelVisibleIncidentsAppliesSearchAndSeverity(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	memStore.UpsertIncident(model.IncidentCard{
		ID:          "panic-a",
		Kind:        "consensus-panic",
		Severity:    model.SeverityCritical,
		Status:      "active",
		Scope:       "validator-a",
		Title:       "Consensus panic on validator-a",
		Summary:     "panic",
		FirstHeight: 10,
		LastHeight:  10,
		UpdatedAt:   time.Now().UTC(),
	})
	memStore.UpsertIncident(model.IncidentCard{
		ID:          "late-b",
		Kind:        "vote-propagation-late",
		Severity:    model.SeverityMedium,
		Status:      "resolved",
		Scope:       "validator-b",
		Title:       "Late receipts on validator-b",
		Summary:     "latency recovered",
		FirstHeight: 11,
		LastHeight:  11,
		UpdatedAt:   time.Now().UTC(),
	})

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})

	require.Len(t, m.visibleIncidents(), 2)

	m.searchQuery = "panic"
	require.Len(t, m.visibleIncidents(), 1)
	require.Equal(t, "panic-a", m.visibleIncidents()[0].card.ID)

	m.searchQuery = ""
	m.severityFilter = model.SeverityCritical
	require.Len(t, m.visibleIncidents(), 1)
	require.Equal(t, "panic-a", m.visibleIncidents()[0].card.ID)
}

func TestModelEnterOpensDetailAndPauseDefersRefresh(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()
	memStore.SetHeightEntry(model.HeightEntry{Height: 10, Report: model.HeightReport{Height: 10, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetHeightEntry(model.HeightEntry{Height: 9, Report: model.HeightReport{Height: 9, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(10)
	memStore.UpsertIncident(model.IncidentCard{
		ID:          "panic-a",
		Kind:        "consensus-panic",
		Severity:    model.SeverityCritical,
		Status:      "active",
		Scope:       "validator-a",
		Title:       "Consensus panic on validator-a",
		Summary:     "panic",
		FirstHeight: 10,
		LastHeight:  10,
		UpdatedAt:   now,
	})

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 120
	m.height = 40
	m.resizeViewport()
	m.refreshViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	require.Equal(t, viewDetail, m.mode)
	require.EqualValues(t, 10, m.selectedHeight)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(Model)
	require.EqualValues(t, 10, m.selectedHeight)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(Model)
	require.EqualValues(t, 9, m.selectedHeight)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	require.True(t, m.paused)

	memStore.SetHeightEntry(model.HeightEntry{Height: 11, Report: model.HeightReport{Height: 11, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(11)

	next, _ = m.Update(storeUpdatedMsg{})
	m = next.(Model)
	require.True(t, m.dirty)
	require.EqualValues(t, 10, m.snap.tip)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	require.False(t, m.paused)
	require.EqualValues(t, 11, m.snap.tip)
}

func TestModelDetailFollowLatestTracksNewHeights(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()
	memStore.SetHeightEntry(model.HeightEntry{Height: 10, Report: model.HeightReport{Height: 10, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetHeightEntry(model.HeightEntry{Height: 9, Report: model.HeightReport{Height: 9, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(10)

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 120
	m.height = 40
	m.resizeViewport()
	m.mode = viewDetail
	m.jumpToLatestHeight()

	require.True(t, m.followLatest)
	require.EqualValues(t, 10, m.selectedHeight)

	memStore.SetHeightEntry(model.HeightEntry{Height: 11, Report: model.HeightReport{Height: 11, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(11)

	next, _ := m.Update(storeUpdatedMsg{})
	m = next.(Model)
	require.True(t, m.followLatest)
	require.EqualValues(t, 11, m.selectedHeight)
	require.EqualValues(t, 11, m.snap.tip)
}

func TestModelDetailNavigationStopsFollowingLatest(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()
	for _, h := range []int64{10, 9, 8} {
		memStore.SetHeightEntry(model.HeightEntry{Height: h, Report: model.HeightReport{Height: h, ChainID: "test-chain"}, LastUpdated: now})
	}
	memStore.SetTip(10)

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 120
	m.height = 40
	m.resizeViewport()
	m.mode = viewDetail
	m.jumpToLatestHeight()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(Model)
	require.False(t, m.followLatest)
	require.EqualValues(t, 9, m.selectedHeight)

	memStore.SetHeightEntry(model.HeightEntry{Height: 11, Report: model.HeightReport{Height: 11, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(11)

	next, _ = m.Update(storeUpdatedMsg{})
	m = next.(Model)
	require.False(t, m.followLatest)
	require.EqualValues(t, 9, m.selectedHeight)
	require.EqualValues(t, 11, m.snap.tip)
}

func TestModelPinnedDetailHeightEventKeepsSelectionWhileTipAdvances(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()
	for _, h := range []int64{160, 159, 158} {
		memStore.SetHeightEntry(model.HeightEntry{Height: h, Report: model.HeightReport{Height: h, ChainID: "test-chain"}, LastUpdated: now})
	}
	memStore.SetTip(160)

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 120
	m.height = 40
	m.resizeViewport()
	m.mode = viewDetail
	m.selectedHeight = 159
	m.followLatest = false
	m.refreshViewport()

	memStore.SetHeightEntry(model.HeightEntry{Height: 161, Report: model.HeightReport{Height: 161, ChainID: "test-chain"}, LastUpdated: now})
	memStore.SetTip(161)

	next, _ := m.Update(storeEventMsg{event: store.StoreEvent{Kind: "height_updated", Height: 161}})
	m = next.(Model)
	require.False(t, m.followLatest)
	require.EqualValues(t, 159, m.selectedHeight)
	require.EqualValues(t, 161, m.snap.tip)
	require.EqualValues(t, 161, m.snap.recentHeights[0].Height)
}

func TestModelHelpViewScrolls(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 80
	m.height = 14
	m.resizeViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = next.(Model)
	require.True(t, m.showHelp)
	require.Equal(t, 0, m.viewport.YOffset)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	require.Greater(t, m.viewport.YOffset, 0)
}

func TestModelCtrlCDoubleQuits(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	m := newModel(Options{Store: memStore, ChainID: "test-chain"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	require.True(t, m.confirmQuit)
	require.False(t, m.quitYes)
	require.Nil(t, cmd)

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	require.NotNil(t, cmd)
	require.IsType(t, tea.QuitMsg{}, cmd())
}

func TestRenderDashboardLimitsIncidentList(t *testing.T) {
	memStore := store.NewMemoryStore(16)
	now := time.Now().UTC()
	memStore.SetTip(12)
	memStore.SetNodeStates([]model.NodeState{
		{Summary: model.NodeSummary{Name: "val1", Role: model.RoleValidator, HighestCommit: 12, CurrentPeers: 3, MaxPeers: 3}},
		{Summary: model.NodeSummary{Name: "val2", Role: model.RoleValidator, HighestCommit: 12, CurrentPeers: 3, MaxPeers: 3}},
	})
	for i := 0; i < 8; i++ {
		memStore.UpsertIncident(model.IncidentCard{
			ID:          "incident-" + string(rune('a'+i)),
			Kind:        "vote-propagation-miss",
			Severity:    model.SeverityMedium,
			Status:      "active",
			Scope:       "val2",
			Title:       "Missing prevote receipts",
			Summary:     "summary",
			FirstHeight: int64(10 + i),
			LastHeight:  int64(10 + i),
			UpdatedAt:   now.Add(time.Duration(i) * time.Second),
		})
	}

	m := newModel(Options{Store: memStore, ChainID: "test-chain"})
	m.width = 100
	m.height = 14

	out := renderDashboard(m)

	require.Contains(t, out, "Nodes")
	require.Contains(t, out, "… ")
	require.LessOrEqual(t, strings.Count(out, "Missing prevote receipts"), 2)
}

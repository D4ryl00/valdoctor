package tui

import (
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

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
)

func TestMemoryStoreActiveIncidentsHaveStableDeterministicOrder(t *testing.T) {
	s := NewMemoryStore(8)
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	s.UpsertIncident(model.IncidentCard{
		ID:          "b",
		Kind:        "vote-propagation-miss",
		Severity:    model.SeverityMedium,
		Status:      "active",
		Title:       "B",
		Scope:       "val2",
		FirstHeight: 10,
		LastHeight:  10,
		UpdatedAt:   now,
	})
	s.UpsertIncident(model.IncidentCard{
		ID:          "a",
		Kind:        "vote-propagation-miss",
		Severity:    model.SeverityMedium,
		Status:      "active",
		Title:       "A",
		Scope:       "val1",
		FirstHeight: 10,
		LastHeight:  10,
		UpdatedAt:   now,
	})

	first := s.ActiveIncidents()
	second := s.ActiveIncidents()

	require.Len(t, first, 2)
	require.Equal(t, []string{"a", "b"}, []string{first[0].ID, first[1].ID})
	require.Equal(t, []string{first[0].ID, first[1].ID}, []string{second[0].ID, second[1].ID})
}

func TestMemoryStoreActiveIncidentsOrderBySeverityThenHeight(t *testing.T) {
	s := NewMemoryStore(8)
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	s.UpsertIncident(model.IncidentCard{
		ID:          "low-newer",
		Kind:        "vote-propagation-miss",
		Severity:    model.SeverityLow,
		Status:      "active",
		Title:       "Low",
		Scope:       "val1",
		FirstHeight: 12,
		LastHeight:  12,
		UpdatedAt:   now.Add(time.Second),
	})
	s.UpsertIncident(model.IncidentCard{
		ID:          "high-older",
		Kind:        "stalled-validator",
		Severity:    model.SeverityHigh,
		Status:      "active",
		Title:       "High",
		Scope:       "val2",
		FirstHeight: 10,
		LastHeight:  10,
		UpdatedAt:   now,
	})
	s.UpsertIncident(model.IncidentCard{
		ID:          "medium-higher-height",
		Kind:        "round-escalation",
		Severity:    model.SeverityMedium,
		Status:      "active",
		Title:       "Medium",
		Scope:       "val3",
		FirstHeight: 20,
		LastHeight:  20,
		UpdatedAt:   now,
	})

	ordered := s.ActiveIncidents()
	require.Equal(t, []string{"high-older", "medium-higher-height", "low-newer"}, []string{
		ordered[0].ID,
		ordered[1].ID,
		ordered[2].ID,
	})
}

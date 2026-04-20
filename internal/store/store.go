package store

import (
	"sort"

	"github.com/D4ryl00/valdoctor/internal/model"
)

// sortNodeStates sorts node states with the canonical validator ordering:
// genesis-indexed nodes first (ascending by genesis index), then unknowns
// alphabetically. This order is used consistently across all TUI views.
func sortNodeStates(states []model.NodeState) {
	sort.SliceStable(states, func(i, j int) bool {
		ii := states[i].Summary.GenesisIndex
		ji := states[j].Summary.GenesisIndex
		switch {
		case ii >= 0 && ji >= 0:
			return ii < ji
		case ii >= 0:
			return true
		case ji >= 0:
			return false
		default:
			return states[i].Summary.Name < states[j].Summary.Name
		}
	})
}

func sortIncidentCards(cards []model.IncidentCard) {
	sort.SliceStable(cards, func(i, j int) bool {
		li := incidentStatusRank(cards[i].Status)
		lj := incidentStatusRank(cards[j].Status)
		if li != lj {
			return li < lj
		}

		si := incidentSeverityRank(cards[i].Severity)
		sj := incidentSeverityRank(cards[j].Severity)
		if si != sj {
			return si < sj
		}

		if cards[i].LastHeight != cards[j].LastHeight {
			return cards[i].LastHeight > cards[j].LastHeight
		}
		if cards[i].FirstHeight != cards[j].FirstHeight {
			return cards[i].FirstHeight > cards[j].FirstHeight
		}
		if !cards[i].UpdatedAt.Equal(cards[j].UpdatedAt) {
			return cards[i].UpdatedAt.After(cards[j].UpdatedAt)
		}
		if cards[i].Kind != cards[j].Kind {
			return cards[i].Kind < cards[j].Kind
		}
		if cards[i].Scope != cards[j].Scope {
			return cards[i].Scope < cards[j].Scope
		}
		if cards[i].Title != cards[j].Title {
			return cards[i].Title < cards[j].Title
		}
		return cards[i].ID < cards[j].ID
	})
}

func incidentSeverityRank(severity model.Severity) int {
	switch severity {
	case model.SeverityCritical:
		return 0
	case model.SeverityHigh:
		return 1
	case model.SeverityMedium:
		return 2
	case model.SeverityLow:
		return 3
	default:
		return 4
	}
}

func incidentStatusRank(status string) int {
	if status == "active" {
		return 0
	}
	return 1
}

type StoreEvent struct {
	Kind       string `json:"kind"`
	Height     int64  `json:"height,omitempty"`
	Node       string `json:"node,omitempty"`
	IncidentID string `json:"incident_id,omitempty"`
}

type Store interface {
	AppendEvent(e model.Event) error
	EventsForHeight(h int64) []model.Event

	SetHeightEntry(e model.HeightEntry) error
	GetHeight(h int64) (model.HeightEntry, bool)
	RecentHeights(limit int) []model.HeightEntry

	SetTip(h int64)
	CurrentTip() int64

	SetNodeStates(states []model.NodeState)
	NodeStates() []model.NodeState
	GetNode(name string) (model.NodeState, bool)

	UpsertIncident(card model.IncidentCard)
	ActiveIncidents() []model.IncidentCard
	RecentResolved(limit int) []model.IncidentCard

	Subscribe() (<-chan StoreEvent, func())
}

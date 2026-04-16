package store

import "github.com/D4ryl00/valdoctor/internal/model"

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

package store

import (
	"slices"
	"sort"
	"sync"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type heightBucket struct {
	events []model.Event
	entry  *model.HeightEntry
}

type MemoryStore struct {
	mu sync.RWMutex

	maxHistory int
	tip        int64

	heights map[int64]*heightBucket
	keys    []int64

	nodes map[string]model.NodeState

	incidents map[string]model.IncidentCard

	subscribers map[int]chan StoreEvent
	nextSubID   int
}

func NewMemoryStore(maxHistory int) *MemoryStore {
	if maxHistory <= 0 {
		maxHistory = 500
	}
	return &MemoryStore{
		maxHistory:  maxHistory,
		heights:     map[int64]*heightBucket{},
		nodes:       map[string]model.NodeState{},
		incidents:   map[string]model.IncidentCard{},
		subscribers: map[int]chan StoreEvent{},
	}
}

func (s *MemoryStore) AppendEvent(e model.Event) error {
	if e.Height <= 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.heights[e.Height]
	if bucket == nil {
		bucket = &heightBucket{}
		s.heights[e.Height] = bucket
		s.keys = appendSortedHeight(s.keys, e.Height)
	}
	bucket.events = append(bucket.events, e)
	return nil
}

func (s *MemoryStore) EventsForHeight(h int64) []model.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket := s.heights[h]
	if bucket == nil || len(bucket.events) == 0 {
		return nil
	}

	out := make([]model.Event, len(bucket.events))
	copy(out, bucket.events)
	return out
}

func (s *MemoryStore) SetHeightEntry(e model.HeightEntry) error {
	s.mu.Lock()
	bucket := s.heights[e.Height]
	if bucket == nil {
		bucket = &heightBucket{}
		s.heights[e.Height] = bucket
		s.keys = appendSortedHeight(s.keys, e.Height)
	}
	entry := e
	bucket.entry = &entry
	s.mu.Unlock()

	s.broadcast(StoreEvent{Kind: "height_updated", Height: e.Height})
	return nil
}

func (s *MemoryStore) GetHeight(h int64) (model.HeightEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bucket := s.heights[h]
	if bucket == nil || bucket.entry == nil {
		return model.HeightEntry{}, false
	}

	return *bucket.entry, true
}

func (s *MemoryStore) RecentHeights(limit int) []model.HeightEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.HeightEntry, 0, len(s.keys))
	for i := len(s.keys) - 1; i >= 0; i-- {
		bucket := s.heights[s.keys[i]]
		if bucket == nil || bucket.entry == nil {
			continue
		}
		out = append(out, *bucket.entry)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *MemoryStore) SetTip(h int64) {
	s.mu.Lock()
	if h <= s.tip {
		s.mu.Unlock()
		return
	}
	s.tip = h
	s.evictLocked()
	s.mu.Unlock()
}

func (s *MemoryStore) CurrentTip() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tip
}

func (s *MemoryStore) SetNodeStates(states []model.NodeState) {
	s.mu.Lock()
	s.nodes = make(map[string]model.NodeState, len(states))
	for _, state := range states {
		s.nodes[state.Summary.Name] = state
	}
	s.mu.Unlock()

	for _, state := range states {
		s.broadcast(StoreEvent{Kind: "node_updated", Node: state.Summary.Name})
	}
}

func (s *MemoryStore) NodeStates() []model.NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.NodeState, 0, len(s.nodes))
	for _, state := range s.nodes {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Summary.Name < out[j].Summary.Name
	})
	return out
}

func (s *MemoryStore) GetNode(name string) (model.NodeState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.nodes[name]
	return state, ok
}

func (s *MemoryStore) UpsertIncident(card model.IncidentCard) {
	s.mu.Lock()
	s.incidents[card.ID] = card
	s.mu.Unlock()

	s.broadcast(StoreEvent{Kind: "incident_updated", IncidentID: card.ID})
}

func (s *MemoryStore) ActiveIncidents() []model.IncidentCard {
	return s.incidentsByStatus("active", 0)
}

func (s *MemoryStore) RecentResolved(limit int) []model.IncidentCard {
	return s.incidentsByStatus("resolved", limit)
}

func (s *MemoryStore) Subscribe() (<-chan StoreEvent, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextSubID
	s.nextSubID++
	ch := make(chan StoreEvent, 64)
	s.subscribers[id] = ch

	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		if sub, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(sub)
		}
	}
}

func (s *MemoryStore) incidentsByStatus(status string, limit int) []model.IncidentCard {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.IncidentCard, 0, len(s.incidents))
	for _, card := range s.incidents {
		if card.Status == status {
			out = append(out, card)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *MemoryStore) evictLocked() {
	if s.maxHistory <= 0 || s.tip <= 0 {
		return
	}

	minHeight := s.tip - int64(s.maxHistory)
	if minHeight <= 0 {
		return
	}

	cut := 0
	for cut < len(s.keys) && s.keys[cut] < minHeight {
		delete(s.heights, s.keys[cut])
		cut++
	}
	if cut > 0 {
		s.keys = slices.Delete(s.keys, 0, cut)
	}
}

func (s *MemoryStore) broadcast(event StoreEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func appendSortedHeight(keys []int64, height int64) []int64 {
	idx, exists := slices.BinarySearch(keys, height)
	if exists {
		return keys
	}
	keys = append(keys, 0)
	copy(keys[idx+1:], keys[idx:])
	keys[idx] = height
	return keys
}

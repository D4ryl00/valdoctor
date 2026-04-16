package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStorePersistsStateAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "live.db")

	sqliteStore, err := NewSQLiteStore(dbPath, 5)
	require.NoError(t, err)

	eventsCh, unsubscribe := sqliteStore.Subscribe()
	defer unsubscribe()

	event := model.Event{
		Height:  3,
		Message: "signed vote",
		Fields: map[string]any{
			"validator": "validator-a",
			"round":     int64(1),
			"ok":        true,
		},
	}
	require.NoError(t, sqliteStore.AppendEvent(event))

	updatedAt := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	require.NoError(t, sqliteStore.SetHeightEntry(model.HeightEntry{
		Height:      3,
		Status:      model.HeightClosed,
		LastUpdated: updatedAt,
		Propagation: model.VotePropagation{
			Height: 3,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{
					Height:          3,
					Round:           1,
					VoteType:        "prevote",
					OriginNode:      "validator-a",
					OriginShortAddr: "abcd1234",
				}: {
					"validator-a": {
						CastAt:     updatedAt,
						ReceivedAt: updatedAt.Add(200 * time.Millisecond),
						Latency:    200 * time.Millisecond,
						Status:     "ok",
					},
				},
			},
		},
	}))

	sqliteStore.SetNodeStates([]model.NodeState{{
		Summary: model.NodeSummary{
			Name:          "validator-a",
			Role:          model.RoleValidator,
			HighestCommit: 3,
		},
		UpdatedAt: updatedAt,
	}})

	sqliteStore.UpsertIncident(model.IncidentCard{
		ID:          "vote-propagation-late-validator-a",
		Kind:        "vote-propagation-late",
		Severity:    model.SeverityMedium,
		Status:      "active",
		Title:       "Late vote propagation",
		Summary:     "validator-a received a vote late",
		FirstHeight: 3,
		LastHeight:  3,
		UpdatedAt:   updatedAt,
	})
	sqliteStore.SetTip(3)

	require.Equal(t, StoreEvent{Kind: "height_updated", Height: 3}, nextStoreEvent(t, eventsCh))
	require.Equal(t, StoreEvent{Kind: "node_updated", Node: "validator-a"}, nextStoreEvent(t, eventsCh))
	require.Equal(t, StoreEvent{Kind: "incident_updated", IncidentID: "vote-propagation-late-validator-a"}, nextStoreEvent(t, eventsCh))

	require.NoError(t, sqliteStore.Close())

	reopened, err := NewSQLiteStore(dbPath, 5)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, reopened.Close())
	}()

	require.EqualValues(t, 3, reopened.CurrentTip())

	events := reopened.EventsForHeight(3)
	require.Len(t, events, 1)
	require.Equal(t, "signed vote", events[0].Message)
	require.Equal(t, "validator-a", events[0].Fields["validator"])
	require.EqualValues(t, 1, events[0].Fields["round"])
	require.Equal(t, true, events[0].Fields["ok"])

	entry, ok := reopened.GetHeight(3)
	require.True(t, ok)
	require.Equal(t, model.HeightClosed, entry.Status)
	require.Len(t, entry.Propagation.Matrix, 1)
	for key, receipts := range entry.Propagation.Matrix {
		require.EqualValues(t, 3, key.Height)
		require.Equal(t, "prevote", key.VoteType)
		require.Equal(t, "ok", receipts["validator-a"].Status)
	}

	node, ok := reopened.GetNode("validator-a")
	require.True(t, ok)
	require.EqualValues(t, 3, node.Summary.HighestCommit)

	active := reopened.ActiveIncidents()
	require.Len(t, active, 1)
	require.Equal(t, "vote-propagation-late-validator-a", active[0].ID)
}

func TestSQLiteStoreEvictionPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "live.db")

	sqliteStore, err := NewSQLiteStore(dbPath, 2)
	require.NoError(t, err)

	for _, height := range []int64{1, 2, 3} {
		require.NoError(t, sqliteStore.AppendEvent(model.Event{
			Height:  height,
			Message: "height event",
		}))
		require.NoError(t, sqliteStore.SetHeightEntry(model.HeightEntry{
			Height:      height,
			LastUpdated: time.Unix(height, 0).UTC(),
		}))
	}

	sqliteStore.SetTip(4)
	require.Nil(t, sqliteStore.EventsForHeight(1))
	_, ok := sqliteStore.GetHeight(1)
	require.False(t, ok)

	recent := sqliteStore.RecentHeights(0)
	require.Len(t, recent, 2)
	require.EqualValues(t, 3, recent[0].Height)
	require.EqualValues(t, 2, recent[1].Height)

	require.NoError(t, sqliteStore.Close())

	reopened, err := NewSQLiteStore(dbPath, 2)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, reopened.Close())
	}()

	require.EqualValues(t, 4, reopened.CurrentTip())
	require.Nil(t, reopened.EventsForHeight(1))
	_, ok = reopened.GetHeight(1)
	require.False(t, ok)

	recent = reopened.RecentHeights(0)
	require.Len(t, recent, 2)
	require.EqualValues(t, 3, recent[0].Height)
	require.EqualValues(t, 2, recent[1].Height)
}

func nextStoreEvent(t *testing.T, ch <-chan StoreEvent) StoreEvent {
	t.Helper()

	select {
	case event := <-ch:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for store event")
		return StoreEvent{}
	}
}

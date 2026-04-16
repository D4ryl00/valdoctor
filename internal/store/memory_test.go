package store

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreEvictsOldHeights(t *testing.T) {
	store := NewMemoryStore(2)

	require.NoError(t, store.AppendEvent(model.Event{Height: 1, Message: "h1"}))
	require.NoError(t, store.AppendEvent(model.Event{Height: 2, Message: "h2"}))
	require.NoError(t, store.AppendEvent(model.Event{Height: 3, Message: "h3"}))

	require.NoError(t, store.SetHeightEntry(model.HeightEntry{Height: 1, LastUpdated: time.Now().UTC()}))
	require.NoError(t, store.SetHeightEntry(model.HeightEntry{Height: 2, LastUpdated: time.Now().UTC()}))
	require.NoError(t, store.SetHeightEntry(model.HeightEntry{Height: 3, LastUpdated: time.Now().UTC()}))

	store.SetTip(4)

	require.Nil(t, store.EventsForHeight(1))
	_, ok := store.GetHeight(1)
	require.False(t, ok)

	recent := store.RecentHeights(0)
	require.Len(t, recent, 2)
	require.EqualValues(t, 3, recent[0].Height)
	require.EqualValues(t, 2, recent[1].Height)
}

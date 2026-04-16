package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/store"
)

func TestStatusAndResourceHandlers(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()
	memStore.SetTip(42)
	memStore.SetNodeStates([]model.NodeState{{
		Summary:   model.NodeSummary{Name: "validator-a", HighestCommit: 42},
		UpdatedAt: now,
	}})
	require.NoError(t, memStore.SetHeightEntry(model.HeightEntry{
		Height:      42,
		Status:      model.HeightClosed,
		Report:      model.HeightReport{Height: 42, ChainID: "chain-a"},
		LastUpdated: now,
	}))
	memStore.UpsertIncident(model.IncidentCard{
		ID:          "panic-a",
		Kind:        "consensus-panic",
		Severity:    model.SeverityCritical,
		Status:      "active",
		Scope:       "validator-a",
		Title:       "Consensus panic on validator-a",
		Summary:     "panic",
		FirstHeight: 42,
		LastHeight:  42,
		UpdatedAt:   now,
	})

	server := httptest.NewServer(withCORS((&Server{
		ChainID: "chain-a",
		Store:   memStore,
	}).routes()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	var status statusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	require.Equal(t, "chain-a", status.ChainID)
	require.EqualValues(t, 42, status.Tip)
	require.Equal(t, 1, status.NodeCount)
	require.Equal(t, 1, status.ActiveIncidentCount)

	resp, err = http.Get(server.URL + "/api/v1/heights?limit=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var heights []heightEntryResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&heights))
	require.Len(t, heights, 1)
	require.EqualValues(t, 42, heights[0].Height)

	resp, err = http.Get(server.URL + "/api/v1/heights/42")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var height heightEntryResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&height))
	require.EqualValues(t, 42, height.Height)

	resp, err = http.Get(server.URL + "/api/v1/heights/42/consensus-text")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var consensus consensusTextResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&consensus))
	require.Contains(t, consensus.Text, "Height 42")

	resp, err = http.Get(server.URL + "/api/v1/incidents?status=active&severity=critical")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var incidents []model.IncidentCard
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&incidents))
	require.Len(t, incidents, 1)
	require.Equal(t, "panic-a", incidents[0].ID)

	resp, err = http.Get(server.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")
}

func TestCORSPreflight(t *testing.T) {
	server := httptest.NewServer(withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/api/v1/status", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "GET, OPTIONS", resp.Header.Get("Access-Control-Allow-Methods"))
}

func TestHeightHandlerSerializesPropagationMatrix(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	now := time.Now().UTC()

	require.NoError(t, memStore.SetHeightEntry(model.HeightEntry{
		Height: 9,
		Status: model.HeightClosed,
		Report: model.HeightReport{Height: 9, ChainID: "chain-a"},
		Propagation: model.VotePropagation{
			Height: 9,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{
					Height:          9,
					Round:           0,
					VoteType:        "prevote",
					OriginNode:      "validator-a",
					OriginShortAddr: "abcd1234",
				}: {
					"validator-a": {
						CastAt:     now,
						ReceivedAt: now.Add(200 * time.Millisecond),
						Latency:    200 * time.Millisecond,
						Status:     "ok",
					},
					"validator-b": {
						Status: "missing",
					},
				},
			},
		},
		LastUpdated: now,
	}))

	server := httptest.NewServer(withCORS((&Server{
		ChainID: "chain-a",
		Store:   memStore,
	}).routes()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/heights/9")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var height heightEntryResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&height))
	require.EqualValues(t, 9, height.Height)
	require.EqualValues(t, 9, height.Propagation.Height)
	require.Len(t, height.Propagation.Votes, 1)
	require.Equal(t, "validator-a", height.Propagation.Votes[0].OriginNode)
	require.Equal(t, "prevote", height.Propagation.Votes[0].VoteType)
	require.Len(t, height.Propagation.Votes[0].Receipts, 2)
	require.Equal(t, "validator-a", height.Propagation.Votes[0].Receipts[0].Receiver)
	require.Equal(t, "ok", height.Propagation.Votes[0].Receipts[0].Status)
	require.Equal(t, "validator-b", height.Propagation.Votes[0].Receipts[1].Receiver)
	require.Equal(t, "missing", height.Propagation.Votes[0].Receipts[1].Status)
}

func TestWebSocketBroadcastsStoreEvents(t *testing.T) {
	memStore := store.NewMemoryStore(8)
	server := httptest.NewServer(withCORS((&Server{
		ChainID: "chain-a",
		Store:   memStore,
	}).routes()))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, memStore.SetHeightEntry(model.HeightEntry{
		Height:      9,
		Status:      model.HeightClosed,
		Report:      model.HeightReport{Height: 9, ChainID: "chain-a"},
		LastUpdated: time.Now().UTC(),
	}))

	var event store.StoreEvent
	require.NoError(t, conn.ReadJSON(&event))
	require.Equal(t, "height_updated", event.Kind)
	require.EqualValues(t, 9, event.Height)
}

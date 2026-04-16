package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	uipkg "github.com/D4ryl00/valdoctor/internal/api/ui"
	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/render"
)

const shutdownTimeout = 5_000_000_000

type statusResponse struct {
	ChainID             string `json:"chain_id"`
	Status              string `json:"status"`
	Tip                 int64  `json:"tip"`
	NodeCount           int    `json:"node_count"`
	ActiveIncidentCount int    `json:"active_incident_count"`
	RecentHeightCount   int    `json:"recent_height_count"`
}

type heightEntryResponse struct {
	Height      int64                   `json:"height"`
	Status      model.HeightStatus      `json:"status"`
	Report      model.HeightReport      `json:"report"`
	Propagation votePropagationResponse `json:"propagation"`
	LastUpdated string                  `json:"last_updated"`
}

type votePropagationResponse struct {
	Height int64                        `json:"height"`
	Votes  []votePropagationRowResponse `json:"votes,omitempty"`
}

type votePropagationRowResponse struct {
	Height          int64                 `json:"height"`
	Round           int                   `json:"round"`
	VoteType        string                `json:"vote_type"`
	OriginNode      string                `json:"origin_node"`
	OriginShortAddr string                `json:"origin_short_addr,omitempty"`
	Receipts        []voteReceiptResponse `json:"receipts,omitempty"`
}

type voteReceiptResponse struct {
	Receiver   string `json:"receiver"`
	CastAt     string `json:"cast_at,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
	LatencyNS  int64  `json:"latency_ns,omitempty"`
	Status     string `json:"status,omitempty"`
}

type consensusTextResponse struct {
	Text string `json:"text"`
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/nodes", s.handleNodes)
	mux.HandleFunc("GET /api/v1/heights", s.handleHeights)
	mux.HandleFunc("GET /api/v1/heights/{height}", s.handleHeight)
	mux.HandleFunc("GET /api/v1/heights/{height}/consensus-text", s.handleConsensusText)
	mux.HandleFunc("GET /api/v1/incidents", s.handleIncidents)
	mux.HandleFunc("GET /api/v1/ws", s.handleWS)
	mux.Handle("/", uipkg.Handler())
	return mux
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{
		ChainID:             s.ChainID,
		Status:              "live",
		Tip:                 s.Store.CurrentTip(),
		NodeCount:           len(s.Store.NodeStates()),
		ActiveIncidentCount: len(s.Store.ActiveIncidents()),
		RecentHeightCount:   len(s.Store.RecentHeights(32)),
	})
}

func (s *Server) handleNodes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.NodeStates())
}

func (s *Server) handleHeights(w http.ResponseWriter, r *http.Request) {
	limit := parsePositiveInt(r.URL.Query().Get("limit"))
	heights := s.Store.RecentHeights(limit)
	payload := make([]heightEntryResponse, 0, len(heights))
	for _, height := range heights {
		payload = append(payload, mapHeightEntry(height))
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleHeight(w http.ResponseWriter, r *http.Request) {
	height, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil || height <= 0 {
		writeError(w, http.StatusBadRequest, "invalid height")
		return
	}

	entry, ok := s.Store.GetHeight(height)
	if !ok {
		writeError(w, http.StatusNotFound, "height not found")
		return
	}

	writeJSON(w, http.StatusOK, mapHeightEntry(entry))
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := query.Get("status")
	severity := model.Severity(query.Get("severity"))
	limit := parsePositiveInt(query.Get("limit"))

	var incidents []model.IncidentCard
	switch status {
	case "", "active":
		incidents = s.Store.ActiveIncidents()
	case "resolved":
		incidents = s.Store.RecentResolved(limit)
	case "all":
		incidents = append(incidents, s.Store.ActiveIncidents()...)
		incidents = append(incidents, s.Store.RecentResolved(limit)...)
	default:
		writeError(w, http.StatusBadRequest, "invalid incident status")
		return
	}

	if severity != "" {
		filtered := make([]model.IncidentCard, 0, len(incidents))
		for _, card := range incidents {
			if card.Severity == severity {
				filtered = append(filtered, card)
			}
		}
		incidents = filtered
	}
	if limit > 0 && len(incidents) > limit {
		incidents = incidents[:limit]
	}

	writeJSON(w, http.StatusOK, incidents)
}

func (s *Server) handleConsensusText(w http.ResponseWriter, r *http.Request) {
	height, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil || height <= 0 {
		writeError(w, http.StatusBadRequest, "invalid height")
		return
	}

	entry, ok := s.Store.GetHeight(height)
	if !ok {
		writeError(w, http.StatusNotFound, "height not found")
		return
	}

	writeJSON(w, http.StatusOK, consensusTextResponse{
		Text: render.HeightText(entry.Report, false),
	})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func parsePositiveInt(raw string) int {
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func mapHeightEntry(entry model.HeightEntry) heightEntryResponse {
	return heightEntryResponse{
		Height:      entry.Height,
		Status:      entry.Status,
		Report:      entry.Report,
		Propagation: mapVotePropagation(entry.Propagation),
		LastUpdated: entry.LastUpdated.UTC().Format(time.RFC3339Nano),
	}
}

func mapVotePropagation(propagation model.VotePropagation) votePropagationResponse {
	rows := make([]votePropagationRowResponse, 0, len(propagation.Matrix))
	for key, receipts := range propagation.Matrix {
		row := votePropagationRowResponse{
			Height:          key.Height,
			Round:           key.Round,
			VoteType:        key.VoteType,
			OriginNode:      key.OriginNode,
			OriginShortAddr: key.OriginShortAddr,
			Receipts:        make([]voteReceiptResponse, 0, len(receipts)),
		}

		receivers := make([]string, 0, len(receipts))
		for receiver := range receipts {
			receivers = append(receivers, receiver)
		}
		sort.Strings(receivers)

		for _, receiver := range receivers {
			receipt := receipts[receiver]
			mapped := voteReceiptResponse{Receiver: receiver}
			if receipt != nil {
				if !receipt.CastAt.IsZero() {
					mapped.CastAt = receipt.CastAt.UTC().Format(time.RFC3339Nano)
				}
				if !receipt.ReceivedAt.IsZero() {
					mapped.ReceivedAt = receipt.ReceivedAt.UTC().Format(time.RFC3339Nano)
				}
				mapped.LatencyNS = int64(receipt.Latency)
				mapped.Status = receipt.Status
			}
			row.Receipts = append(row.Receipts, mapped)
		}

		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Height != rows[j].Height {
			return rows[i].Height < rows[j].Height
		}
		if rows[i].Round != rows[j].Round {
			return rows[i].Round < rows[j].Round
		}
		if rows[i].VoteType != rows[j].VoteType {
			return rows[i].VoteType < rows[j].VoteType
		}
		if rows[i].OriginNode != rows[j].OriginNode {
			return rows[i].OriginNode < rows[j].OriginNode
		}
		return rows[i].OriginShortAddr < rows[j].OriginShortAddr
	})

	return votePropagationResponse{
		Height: propagation.Height,
		Votes:  rows,
	}
}

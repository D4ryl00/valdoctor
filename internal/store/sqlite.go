package store

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/D4ryl00/valdoctor/internal/model"
)

const sqliteTimeLayout = time.RFC3339Nano

func init() {
	gob.Register(time.Time{})
	gob.Register(map[string]any{})
}

type SQLiteStore struct {
	db *sql.DB

	maxHistory int

	mu          sync.RWMutex
	subscribers map[int]chan StoreEvent
	nextSubID   int
}

var _ Store = (*SQLiteStore)(nil)
var _ io.Closer = (*SQLiteStore)(nil)

func NewSQLiteStore(path string, maxHistory int) (*SQLiteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if maxHistory <= 0 {
		maxHistory = 500
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteStore{
		db:          db,
		maxHistory:  maxHistory,
		subscribers: map[int]chan StoreEvent{},
	}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) AppendEvent(e model.Event) error {
	if e.Height <= 0 {
		return nil
	}
	payload, err := encodeGob(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO events(height, payload) VALUES(?, ?)`, e.Height, payload)
	return err
}

func (s *SQLiteStore) EventsForHeight(h int64) []model.Event {
	rows, err := s.db.Query(`SELECT payload FROM events WHERE height = ? ORDER BY id ASC`, h)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]model.Event, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		var event model.Event
		if err := decodeGob(payload, &event); err != nil {
			continue
		}
		out = append(out, event)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *SQLiteStore) SetHeightEntry(e model.HeightEntry) error {
	payload, err := encodeGob(e)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO heights(height, status, updated_at, payload)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(height) DO UPDATE SET
			status = excluded.status,
			updated_at = excluded.updated_at,
			payload = excluded.payload
	`, e.Height, int(e.Status), e.LastUpdated.UTC().Format(sqliteTimeLayout), payload)
	if err != nil {
		return err
	}

	s.broadcast(StoreEvent{Kind: "height_updated", Height: e.Height})
	return nil
}

func (s *SQLiteStore) GetHeight(h int64) (model.HeightEntry, bool) {
	var payload []byte
	err := s.db.QueryRow(`SELECT payload FROM heights WHERE height = ?`, h).Scan(&payload)
	if err != nil {
		return model.HeightEntry{}, false
	}

	var entry model.HeightEntry
	if err := decodeGob(payload, &entry); err != nil {
		return model.HeightEntry{}, false
	}
	return entry, true
}

func (s *SQLiteStore) RecentHeights(limit int) []model.HeightEntry {
	query := `SELECT payload FROM heights ORDER BY height DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]model.HeightEntry, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		var entry model.HeightEntry
		if err := decodeGob(payload, &entry); err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (s *SQLiteStore) SetTip(h int64) {
	current := s.CurrentTip()
	if h <= current {
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO meta(key, value)
		VALUES('tip', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, h); err != nil {
		return
	}

	if s.maxHistory > 0 {
		minHeight := h - int64(s.maxHistory)
		if minHeight > 0 {
			if _, err := tx.Exec(`DELETE FROM events WHERE height < ?`, minHeight); err != nil {
				return
			}
			if _, err := tx.Exec(`DELETE FROM heights WHERE height < ?`, minHeight); err != nil {
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return
	}
}

func (s *SQLiteStore) CurrentTip() int64 {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'tip'`).Scan(&raw)
	if err != nil {
		return 0
	}

	tip, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return tip
}

func (s *SQLiteStore) SetNodeStates(states []model.NodeState) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM nodes`); err != nil {
		return
	}
	for _, state := range states {
		payload, err := encodeGob(state)
		if err != nil {
			return
		}
		if _, err := tx.Exec(`
			INSERT INTO nodes(name, updated_at, payload)
			VALUES(?, ?, ?)
		`, state.Summary.Name, state.UpdatedAt.UTC().Format(sqliteTimeLayout), payload); err != nil {
			return
		}
	}

	if err := tx.Commit(); err != nil {
		return
	}

	for _, state := range states {
		s.broadcast(StoreEvent{Kind: "node_updated", Node: state.Summary.Name})
	}
}

func (s *SQLiteStore) NodeStates() []model.NodeState {
	rows, err := s.db.Query(`SELECT payload FROM nodes ORDER BY name ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]model.NodeState, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		var state model.NodeState
		if err := decodeGob(payload, &state); err != nil {
			continue
		}
		out = append(out, state)
	}
	return out
}

func (s *SQLiteStore) GetNode(name string) (model.NodeState, bool) {
	var payload []byte
	err := s.db.QueryRow(`SELECT payload FROM nodes WHERE name = ?`, name).Scan(&payload)
	if err != nil {
		return model.NodeState{}, false
	}

	var state model.NodeState
	if err := decodeGob(payload, &state); err != nil {
		return model.NodeState{}, false
	}
	return state, true
}

func (s *SQLiteStore) UpsertIncident(card model.IncidentCard) {
	payload, err := encodeGob(card)
	if err != nil {
		return
	}

	_, err = s.db.Exec(`
		INSERT INTO incidents(id, status, updated_at, severity, first_height, last_height, payload)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			updated_at = excluded.updated_at,
			severity = excluded.severity,
			first_height = excluded.first_height,
			last_height = excluded.last_height,
			payload = excluded.payload
	`, card.ID, card.Status, card.UpdatedAt.UTC().Format(sqliteTimeLayout), string(card.Severity), card.FirstHeight, card.LastHeight, payload)
	if err != nil {
		return
	}

	s.broadcast(StoreEvent{Kind: "incident_updated", IncidentID: card.ID})
}

func (s *SQLiteStore) ActiveIncidents() []model.IncidentCard {
	return s.incidentsByStatus("active", 0)
}

func (s *SQLiteStore) RecentResolved(limit int) []model.IncidentCard {
	return s.incidentsByStatus("resolved", limit)
}

func (s *SQLiteStore) Subscribe() (<-chan StoreEvent, func()) {
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

func (s *SQLiteStore) incidentsByStatus(status string, limit int) []model.IncidentCard {
	query := `SELECT payload FROM incidents WHERE status = ? ORDER BY updated_at DESC`
	args := []any{status}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]model.IncidentCard, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		var card model.IncidentCard
		if err := decodeGob(payload, &card); err != nil {
			continue
		}
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (s *SQLiteStore) initSchema() error {
	stmts := []string{
		`PRAGMA journal_mode = WAL;`,
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			height INTEGER NOT NULL,
			payload BLOB NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_events_height ON events(height);`,
		`CREATE TABLE IF NOT EXISTS heights (
			height INTEGER PRIMARY KEY,
			status INTEGER NOT NULL,
			updated_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS nodes (
			name TEXT PRIMARY KEY,
			updated_at TEXT NOT NULL,
			payload BLOB NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS incidents (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			severity TEXT NOT NULL,
			first_height INTEGER NOT NULL,
			last_height INTEGER NOT NULL,
			payload BLOB NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_incidents_status_updated_at ON incidents(status, updated_at DESC);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("initializing sqlite schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) broadcast(event StoreEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func encodeGob(value any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGob(payload []byte, dest any) error {
	return gob.NewDecoder(bytes.NewReader(payload)).Decode(dest)
}

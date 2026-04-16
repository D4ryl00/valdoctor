package model

import "time"

type HeightStatus int

const (
	HeightActive HeightStatus = iota
	HeightClosed
	HeightEvicted
)

type HeightEntry struct {
	Height      int64           `json:"height"`
	Status      HeightStatus    `json:"status"`
	Report      HeightReport    `json:"report"`
	Propagation VotePropagation `json:"propagation"`
	LastUpdated time.Time       `json:"last_updated"`
}

type ClosurePolicy int

const (
	PolicySingleValidatorCommit ClosurePolicy = iota
	PolicyObservedValidatorMajority
	PolicyObservedAllValidatorSources
)

type ValidatorIdentity struct {
	NodeName     string `json:"node_name"`
	ShortAddr    string `json:"short_addr,omitempty"`
	FullAddr     string `json:"full_addr,omitempty"`
	GenesisIndex int    `json:"genesis_index,omitempty"`
	IsValidator  bool   `json:"is_validator"`
}

type VoteKey struct {
	Height          int64  `json:"height"`
	Round           int    `json:"round"`
	VoteType        string `json:"vote_type"`
	OriginNode      string `json:"origin_node"`
	OriginShortAddr string `json:"origin_short_addr"`
}

type VoteReceipt struct {
	CastAt     time.Time     `json:"cast_at,omitempty"`
	ReceivedAt time.Time     `json:"received_at,omitempty"`
	Latency    time.Duration `json:"latency_ns,omitempty"`
	Status     string        `json:"status,omitempty"`
}

type VotePropagation struct {
	Height int64                               `json:"height"`
	Matrix map[VoteKey]map[string]*VoteReceipt `json:"matrix,omitempty"`
}

type IncidentCard struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Severity    Severity   `json:"severity"`
	Status      string     `json:"status"`
	Scope       string     `json:"scope"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	FirstHeight int64      `json:"first_height"`
	LastHeight  int64      `json:"last_height"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Evidence    []Evidence `json:"evidence,omitempty"`
}

type NodeState struct {
	Summary   NodeSummary `json:"summary"`
	UpdatedAt time.Time   `json:"updated_at"`
}

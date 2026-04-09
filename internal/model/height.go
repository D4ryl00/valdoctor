package model

import "time"

// VoteKind classifies a validator's observed vote behaviour in one consensus round.
type VoteKind string

const (
	VoteAbsent     VoteKind = "absent"      // no vote observed for this validator
	VoteNil        VoteKind = "nil"         // voted nil
	VoteBlock      VoteKind = "block"       // voted for the round's proposed block
	VoteLateBlock  VoteKind = "late_block"  // voted for the block after the step timeout fired
	VoteLateNil    VoteKind = "late_nil"    // voted nil after the step timeout fired
	VoteOtherBlock VoteKind = "other_block" // voted for a different block hash than the proposal
)

// VoteEntry holds the prevote and precommit outcome for one validator in one round.
type VoteEntry struct {
	Prevote   VoteKind
	Precommit VoteKind
}

// ValidatorVoteRow holds per-round vote outcomes for a single validator slot.
type ValidatorVoteRow struct {
	Index int    // genesis slot index (0-based)
	Name  string // validator name from genesis
	Addr  string // bech32 address from genesis

	// ByRound maps round number to the vote outcome for that round.
	ByRound map[int]VoteEntry
}

// RoundSummary captures what happened in one consensus round at a given height.
type RoundSummary struct {
	Round int

	// Proposal
	ProposalSeen      bool
	ProposalHash      string // truncated to 8 hex chars for display
	ProposalFromRound int    // >0 when the proposal re-uses a POL block from an earlier round
	ProposalValid     bool

	// Prevote aggregate (across all observed logs for this round)
	PrevotesForBlock int
	PrevotesNil      int
	PrevotesOther    int
	PrevotesTotal    int    // total observed; may be < full set when logs are incomplete
	PrevotesMaj23    bool   // +2/3 majority reached
	PrevoteMaj23Hash string // block hash that achieved +2/3; empty = nil majority

	// Precommit aggregate
	PrecommitsForBlock int
	PrecommitsNil      int
	PrecommitsOther    int
	PrecommitsTotal    int
	PrecommitsMaj23    bool

	// Human-readable narratives derived from classified log events.
	PrevoteNarrative   string
	PrecommitNarrative string

	// Round outcome
	Committed bool // FinalizeCommit observed at this round
	TimedOut  bool // at least one timeout event observed at this (height, round)
}

// PeerEvent records a single peer connection change during the block's consensus window.
type PeerEvent struct {
	Timestamp time.Time
	Node      string // which node's log this came from
	Added     bool   // true = added, false = dropped
	PeerAddr  string // bech32 address or raw p2p ID
	PeerName  string // resolved from metadata.PeerAliases (empty if unknown)
	PeerRole  Role   // validator, sentry, unknown
	ErrReason string // non-empty for dropped peers
}

// ClockSyncRow holds clock-synchronisation data for one node at a specific height.
type ClockSyncRow struct {
	Node               string
	EnterProposeTime   time.Time // EnterPropose(H, 0) timestamp; zero if not observed
	FinalizeCommitTime time.Time // FinalizeCommit(H) timestamp; zero if not observed
	// DeltaMs is the delta from the median FinalizeCommit time across all nodes.
	// Positive = this node committed later than median; negative = earlier.
	DeltaMs int64
	Status  string // "ok" (<200ms), "warn" (200ms–1s), "critical" (>1s), "unknown"
}

// BlockHeader holds block header data fetched from RPC /block.
type BlockHeader struct {
	Time         time.Time
	Hash         string
	ProposerAddr string
	ProposerName string // resolved from genesis + metadata; empty if not resolvable
	TxCount      int
	AppHash      string
}

// CommitSig records whether a validator signed the final commit of a block.
type CommitSig struct {
	Index         int    // genesis slot index
	ValidatorAddr string // hex or bech32 from /commit response
	ValidatorName string // resolved from genesis + metadata
	Signed        bool
	Round         int // the round at which this validator precommitted
}

// TxSummary holds the execution result of a single transaction at a given height.
type TxSummary struct {
	GasWanted int64
	GasUsed   int64
	Error     string // empty if successful
}

// HeightReport is the full analysis output produced by the height subcommand.
type HeightReport struct {
	Height  int64
	ChainID string

	// Block header from RPC /block (nil when --offline or RPC unavailable).
	Block *BlockHeader

	// Commit signatures from RPC /commit (nil when --offline or unavailable).
	CommitSigs []CommitSig

	// Transaction results from RPC /block_results (nil when --offline or unavailable).
	TxResults []TxSummary

	// Consensus rounds derived from logs (enriched with RPC data where available).
	Rounds []RoundSummary

	// Per-validator vote grid. Length matches genesis validator count; absent
	// validators still have a row (with VoteAbsent entries).
	ValidatorVotes []ValidatorVoteRow

	// Peer connection events that occurred during the block's consensus window.
	PeerEvents      []PeerEvent
	PeerWindowStart time.Time // start of the analysis window (EnterPropose or H-1 commit)
	PeerWindowEnd   time.Time // end of the analysis window (FinalizeCommit at H)

	// Clock synchronisation across nodes (one row per node with log coverage).
	ClockSync []ClockSyncRow

	// True if a conflicting-vote (equivocation) event was observed at this height.
	DoubleSignDetected bool

	// FocusNode is the node name for single-node view; empty = aggregate.
	FocusNode string

	Warnings []string
}

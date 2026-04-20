package analyze

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

// TestBuildReportMissingCommitBlockSingle verifies that a single occurrence of
// EventCommitBlockMissing is not flagged — it is transient during catch-up.
func TestBuildReportMissingCommitBlockSingle(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test5",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         10,
				Message:      "Attempt to finalize failed. We don't have the commit block.",
				Kind:         model.EventCommitBlockMissing,
			},
		},
	})

	require.False(t, report.CriticalIssuesDetected)
	for _, f := range report.Findings {
		require.NotEqual(t, "missing-commit-block-validator_1", f.ID)
	}
}

// TestBuildReportMissingCommitBlockRepeated verifies that three or more
// occurrences of EventCommitBlockMissing produce a high-severity finding.
func TestBuildReportMissingCommitBlockRepeated(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	events := make([]model.Event, 3)
	for i := range events {
		events[i] = model.Event{
			Timestamp:    now.Add(time.Duration(i) * time.Second),
			HasTimestamp: true,
			Node:         "validator_1",
			Role:         model.RoleValidator,
			Path:         "/tmp/validator.log",
			Line:         10 + i,
			Message:      "Attempt to finalize failed. We don't have the commit block.",
			Kind:         model.EventCommitBlockMissing,
		}
	}
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test5",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: events,
	})

	found := false
	for _, f := range report.Findings {
		if f.ID == "missing-commit-block-validator_1" {
			require.Equal(t, model.SeverityHigh, f.Severity)
			found = true
		}
	}
	require.True(t, found, "expected missing-commit-block finding")
}

func TestBuildReportSignVoteConflict(t *testing.T) {
	now := time.Date(2026, 4, 7, 19, 32, 5, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test12",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         87,
				Message:      "Error signing vote",
				Fields: map[string]any{
					"err": "same HRS with conflicting data",
				},
				Kind:   model.EventSignVoteError,
				Height: 234888,
			},
		},
	})

	found := false
	for _, f := range report.Findings {
		if f.ID == "sign-vote-conflict-validator_1" {
			require.Equal(t, model.SeverityCritical, f.Severity)
			found = true
		}
	}
	require.True(t, found, "expected sign-vote-conflict finding")
}

func TestBuildReportConsensusWALIssue(t *testing.T) {
	now := time.Date(2026, 4, 7, 19, 32, 5, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test12",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         55,
				Message:      "Error on catchup replay. Proceeding to start ConsensusState anyway",
				Fields: map[string]any{
					"err": "cannot replay height 234888. WAL does not contain #ENDHEIGHT for 234887",
				},
				Kind: model.EventConsensusWALIssue,
			},
		},
	})

	found := false
	for _, f := range report.Findings {
		if f.ID == "consensus-wal-issue-validator_1" {
			require.Equal(t, model.SeverityHigh, f.Severity)
			found = true
		}
	}
	require.True(t, found, "expected consensus-wal-issue finding")
}

func TestBuildReportSuppressesInitialConsensusWALBootstrapNoise(t *testing.T) {
	now := time.Date(2026, 4, 20, 16, 19, 36, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test17",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         55,
				Message:      "Error on catchup replay. Proceeding to start ConsensusState anyway",
				Fields: map[string]any{
					"err": "cannot replay height 1. WAL does not contain #ENDHEIGHT for 0",
				},
				Kind: model.EventConsensusWALIssue,
			},
			{
				Timestamp:    now.Add(time.Second),
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         56,
				Message:      "Finalizing commit of block",
				Kind:         model.EventFinalizeCommit,
				Height:       1,
			},
		},
	})

	for _, f := range report.Findings {
		require.NotEqual(t, "consensus-wal-issue-validator_1", f.ID)
	}
}

func TestBuildReportSignProposalError(t *testing.T) {
	now := time.Date(2026, 4, 7, 19, 32, 5, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test12",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         99,
				Message:      "enterPropose: Error signing proposal",
				Fields: map[string]any{
					"err": "remote signer unavailable",
				},
				Kind:   model.EventSignProposalError,
				Height: 234888,
				Round:  2,
			},
		},
	})

	found := false
	for _, f := range report.Findings {
		if f.ID == "sign-proposal-error-validator_1" {
			require.Equal(t, model.SeverityHigh, f.Severity)
			found = true
		}
	}
	require.True(t, found, "expected sign-proposal-error finding")
}

func TestBuildReportPeerConfigError(t *testing.T) {
	now := time.Date(2026, 4, 7, 19, 32, 5, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test12",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now,
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         3,
				Message:      "invalid persistent peer address",
				Kind:         model.EventPeerConfigError,
			},
		},
	})

	found := false
	for _, f := range report.Findings {
		if f.ID == "peer-config-error-validator_1" {
			require.Equal(t, model.SeverityMedium, f.Severity)
			found = true
		}
	}
	require.True(t, found, "expected peer-config-error finding")
}

func TestBuildReportSuppressesPeerStarvationDuringGracefulShutdown(t *testing.T) {
	now := time.Date(2026, 4, 20, 16, 20, 28, 0, time.UTC)
	report := BuildReport(Input{
		Genesis: model.Genesis{
			Path:         "/tmp/genesis.json",
			ChainID:      "test17",
			ValidatorNum: 1,
		},
		Sources: []model.Source{
			{Path: "/tmp/validator.log", Node: "validator_1", Role: model.RoleValidator},
		},
		Events: []model.Event{
			{
				Timestamp:    now.Add(-3 * time.Second),
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         10,
				Message:      "Added peer",
				Kind:         model.EventAddedPeer,
			},
			{
				Timestamp:    now.Add(-2 * time.Second),
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         11,
				Message:      "Timed out",
				Kind:         model.EventTimeout,
				Height:       9,
			},
			{
				Timestamp:    now.Add(-1500 * time.Millisecond),
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         12,
				Message:      "Stopping peer",
				Kind:         model.EventStoppedPeer,
			},
			{
				Timestamp:    now.Add(-time.Second),
				HasTimestamp: true,
				Node:         "validator_1",
				Role:         model.RoleValidator,
				Path:         "/tmp/validator.log",
				Line:         13,
				Message:      "Stopping Node",
				Kind:         model.EventNodeShutdown,
			},
		},
	})

	for _, f := range report.Findings {
		require.NotEqual(t, "peer-starvation-validator_1", f.ID)
	}
}

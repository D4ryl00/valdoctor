package tui

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestReceiptCellRendersQuorumSatisfied(t *testing.T) {
	require.Equal(t, "after+2/3", receiptCell(&model.VoteReceipt{
		Status:     "quorum-satisfied",
		Latency:    250 * time.Millisecond,
		CastAt:     time.Unix(1, 0),
		ReceivedAt: time.Unix(1, 0).Add(250 * time.Millisecond),
	}))
}

func TestRenderConsensusContentDropsDuplicateHeightHeader(t *testing.T) {
	content := renderConsensusContent(model.HeightEntry{
		Height: 42,
		Report: model.HeightReport{
			Height:         42,
			ChainID:        "test-chain",
			CommittedInLog: false,
			Rounds: []model.RoundSummary{{
				Round:           0,
				ProposalSeen:    true,
				ProposalHash:    "ABCDEF12",
				ProposalValid:   true,
				PrevoteNarrative: "2/3 block",
				PrecommitNarrative: "not reached",
			}},
		},
	}, newStyles(false))

	require.NotContains(t, content, "Height 42 — chain test-chain")
	require.Contains(t, content, "Consensus narrative")
}

func TestRenderPropagationContentUsesSharedSectionHeader(t *testing.T) {
	content := renderPropagationContent(model.HeightEntry{
		Height: 58,
		Propagation: model.VotePropagation{
			Height: 58,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 58, Round: 0, VoteType: "prevote", OriginNode: "val1"}: {
					"val1": {Status: "ok"},
				},
			},
		},
	}, []model.NodeState{{Summary: model.NodeSummary{Name: "val1", ShortAddr: "g1abc123", GenesisIndex: 0}}}, newStyles(false))

	require.Contains(t, content, "Vote propagation matrix — h58")
	require.Contains(t, content, "Legend:")
}

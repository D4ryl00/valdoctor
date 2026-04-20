package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
)

func TestRenderPropagationContentOrdersPrevoteBeforePrecommit(t *testing.T) {
	entry := model.HeightEntry{
		Height: 10,
		Propagation: model.VotePropagation{
			Height: 10,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{
					Height:     10,
					Round:      0,
					VoteType:   "precommit",
					OriginNode: "val-a",
				}: {
					"val-b": {Status: "ok"},
				},
				{
					Height:     10,
					Round:      0,
					VoteType:   "prevote",
					OriginNode: "val-a",
				}: {
					"val-b": {Status: "ok"},
				},
			},
		},
	}
	nodes := []model.NodeState{
		{Summary: model.NodeSummary{Name: "val-a", GenesisIndex: 0}},
		{Summary: model.NodeSummary{Name: "val-b", GenesisIndex: 1}},
	}

	out := renderPropagationContent(entry, nodes)

	prevoteIdx := strings.Index(out, "r0 pv")
	precommitIdx := strings.Index(out, "r0 pc")
	require.NotEqual(t, -1, prevoteIdx)
	require.NotEqual(t, -1, precommitIdx)
	require.Less(t, prevoteIdx, precommitIdx)
}

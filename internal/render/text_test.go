package render

import (
	"strings"
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTextRendersVoteBitmapLabels(t *testing.T) {
	report := model.Report{
		Input: model.InputSummary{
			ChainID:        "test5",
			ValidatorCount: 4,
			LogFileCount:   1,
			NodeCount:      1,
		},
		ValidatorSlots: []model.ValidatorSlot{
			{Index: 1, Node: "val-a"},
			{Index: 2, Node: "val-b"},
			{Index: 3, Node: "val-c"},
			{Index: 4, Node: "val-d"},
		},
		Nodes: []model.NodeSummary{
			{
				Name:               "val-a",
				Role:               model.RoleValidator,
				LastHeight:         12,
				PrevotesReceived:   3,
				PrevotesTotal:      4,
				PrevotesBitArray:   "x_xx",
				PrecommitsReceived: 2,
				PrecommitsTotal:    4,
				PrecommitsBitArray: "x__x",
			},
		},
	}

	out := Text(report, TextOptions{})

	require.True(t, strings.Contains(out, "prevote bitmap: x_xx"))
	require.True(t, strings.Contains(out, "prevote: missing 2:val-b"))
	require.True(t, strings.Contains(out, "precommit bitmap: x__x"))
	require.True(t, strings.Contains(out, "precommit: missing 2:val-b, 3:val-c"))
}

func TestTextRendersUnavailableVoteBitmap(t *testing.T) {
	report := model.Report{
		Input: model.InputSummary{
			ChainID:        "test5",
			ValidatorCount: 4,
			LogFileCount:   1,
			NodeCount:      1,
		},
		Nodes: []model.NodeSummary{
			{
				Name:             "val-a",
				Role:             model.RoleValidator,
				LastHeight:       12,
				PrevotesReceived: 3,
				PrevotesTotal:    4,
			},
		},
	}

	out := Text(report, TextOptions{})

	require.True(t, strings.Contains(out, "prevote bitmap: unavailable"))
	require.True(t, strings.Contains(out, "precommit bitmap: unavailable"))
}

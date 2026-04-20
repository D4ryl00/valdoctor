package analyze

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/rpc"
	"github.com/stretchr/testify/require"
)

func TestBuildValidatorVoteGridKeepsDroppedPrecommitAbsent(t *testing.T) {
	base := time.Date(2026, 4, 20, 21, 43, 14, 0, time.UTC)
	events := []model.Event{
		{
			Height: 160,
			Round:  0,
			Kind:   model.EventSignVoteError,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vidx":      0,
			},
		},
		{
			Height: 160,
			Round:  0,
			Kind:   model.EventSignVoteError,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vidx":      1,
			},
		},
		{
			Node:         "val3",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vidx":      2,
				"_vhash":     "3A2534EFFDE9",
			},
		},
		{
			Node:         "val4",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vidx":      4,
				"_vhash":     "3A2534EFFDE9",
			},
		},
		{
			Node:         "val5",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vidx":      3,
				"_vhash":     "3A2534EFFDE9",
			},
		},
		{
			Node:         "val1",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(10 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vbits":     "__xxx",
				"_vrecv":     3,
				"_vtotal":    5,
				"_vmaj23":    false,
			},
		},
	}

	grid := buildValidatorVoteGrid(events, model.Genesis{
		Validators: []model.Validator{
			{Name: "val1", Address: "g1psxnzez9fpxj4vspy948j29t6mhlcheyj73h3g"},
			{Name: "val2", Address: "g19e4uzxhv6r0ernkvswmx4a5g3fsz43gv3fe2n7"},
			{Name: "val3", Address: "g1842e00juvabcdefghijklmno1234567890abcd"},
			{Name: "val5", Address: "g1s60vuacd8abcdefghijklmno1234567890abcd"},
			{Name: "val4", Address: "g17e6mpr72xabcdefghijklmno1234567890abcd"},
		},
	}, model.Metadata{}, []rpc.ValidatorEntry{
		{Address: "g1psxnzez9fpxj4vspy948j29t6mhlcheyj73h3g"},
		{Address: "g19e4uzxhv6r0ernkvswmx4a5g3fsz43gv3fe2n7"},
		{Address: "g1842e00juvabcdefghijklmno1234567890abcd"},
		{Address: "g1s60vuacd8abcdefghijklmno1234567890abcd"},
		{Address: "g17e6mpr72xabcdefghijklmno1234567890abcd"},
	})

	require.Equal(t, model.VoteAbsent, grid[0].ByRound[0].Precommit)
	require.Equal(t, model.VoteAbsent, grid[1].ByRound[0].Precommit)
	require.Equal(t, model.VoteBlock, grid[2].ByRound[0].Precommit)
	require.Equal(t, model.VoteBlock, grid[3].ByRound[0].Precommit)
	require.Equal(t, model.VoteBlock, grid[4].ByRound[0].Precommit)
}

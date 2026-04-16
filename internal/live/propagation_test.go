package live

import (
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBuildPropagationStatuses(t *testing.T) {
	resolver := &IdentityResolver{
		Sources: []model.Source{
			{Node: "validator-a", Role: model.RoleValidator},
			{Node: "validator-b", Role: model.RoleValidator},
			{Node: "validator-c", Role: model.RoleValidator},
		},
		Metadata: model.Metadata{
			Nodes: map[string]model.MetadataNode{
				"validator-a": {ValidatorAddress: "AAAAAAAAAAAA0000000000000000000000000000"},
				"validator-b": {ValidatorAddress: "BBBBBBBBBBBB0000000000000000000000000000"},
				"validator-c": {ValidatorAddress: "CCCCCCCCCCCC0000000000000000000000000000"},
			},
		},
	}

	base := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		{
			Node:   "validator-a",
			Height: 10,
			Round:  0,
			Kind:   model.EventSignedVote,
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vaddrprefix": "AAAAAAAAAAAA",
				"_cast_at":     base,
			},
		},
		{
			Node:         "validator-b",
			Height:       10,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			Timestamp:    base.Add(100 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vaddrprefix": "AAAAAAAAAAAA",
			},
		},
		{
			Node:   "validator-b",
			Height: 10,
			Round:  1,
			Kind:   model.EventSignedVote,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "BBBBBBBBBBBB",
				"_cast_at":     base.Add(time.Second),
			},
		},
		{
			Node:         "validator-a",
			Height:       10,
			Round:        1,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(time.Second + 600*time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "BBBBBBBBBBBB",
			},
		},
		{
			Node:         "validator-c",
			Height:       10,
			Round:        2,
			Kind:         model.EventAddedPrevote,
			Timestamp:    base.Add(2 * time.Second),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vaddrprefix": "BBBBBBBBBBBB",
			},
		},
	}

	prop := BuildPropagation(events, 10, resolver, []string{"validator-a", "validator-b", "validator-c"}, true)

	keyOK := model.VoteKey{Height: 10, Round: 0, VoteType: "prevote", OriginNode: "validator-a", OriginShortAddr: "AAAAAAAAAAAA"}
	require.Equal(t, "ok", prop.Matrix[keyOK]["validator-a"].Status)
	require.Equal(t, "ok", prop.Matrix[keyOK]["validator-b"].Status)
	require.Equal(t, "missing", prop.Matrix[keyOK]["validator-c"].Status)

	keyLate := model.VoteKey{Height: 10, Round: 1, VoteType: "precommit", OriginNode: "validator-b", OriginShortAddr: "BBBBBBBBBBBB"}
	require.Equal(t, "late", prop.Matrix[keyLate]["validator-a"].Status)
	require.Equal(t, "ok", prop.Matrix[keyLate]["validator-b"].Status)
	require.Equal(t, "missing", prop.Matrix[keyLate]["validator-c"].Status)

	keyUnknown := model.VoteKey{Height: 10, Round: 2, VoteType: "prevote", OriginNode: "validator-b", OriginShortAddr: "BBBBBBBBBBBB"}
	require.Equal(t, "unknown_cast_time", prop.Matrix[keyUnknown]["validator-a"].Status)
	require.Equal(t, "unknown_cast_time", prop.Matrix[keyUnknown]["validator-b"].Status)
	require.Equal(t, "unknown_cast_time", prop.Matrix[keyUnknown]["validator-c"].Status)
}

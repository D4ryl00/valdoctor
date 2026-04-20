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

// TestBuildPropagationBitArrayFallback verifies that when log events only carry
// VoteSet BitArray snapshots (no per-vote _vaddrprefix), the BA progression pass
// infers receipt times from BA transitions.
func TestBuildPropagationBitArrayFallback(t *testing.T) {
	// Genesis: val1=slot0, val2=slot1, val3=slot2
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
				{Name: "val3"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)

	// val1 signs a prevote at base.
	// val2 logs two BA snapshots: first only val1 bit (slot 0), then all three.
	// val3 logs one snapshot with all three bits set at base+300ms.
	events := []model.Event{
		{
			Node:         "val1",
			Height:       5,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base,
			},
		},
		// val2 receives val1's prevote at +100ms (BA x__ → x_x would not yet be here)
		{
			Node:         "val2",
			Height:       5,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(100 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vbits":     "x__",
				"_vrecv":     1,
				"_vtotal":    3,
				"_vmaj23":    false,
			},
		},
		// val2 later receives val3's prevote at +250ms (BA x__ → x_x)
		{
			Node:         "val2",
			Height:       5,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(250 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":  "prevote",
				"_vbits":      "x_x",
				"_vrecv":      2,
				"_vtotal":     3,
				"_vmaj23":     false,
				"_vmaj23hash": "",
			},
		},
		// val3 receives val1's prevote at +300ms
		{
			Node:         "val3",
			Height:       5,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(300 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vbits":     "x__",
				"_vrecv":     1,
				"_vtotal":    3,
				"_vmaj23":    false,
			},
		},
	}

	prop := BuildPropagation(events, 5, resolver, []string{"val1", "val2", "val3"}, true)

	keyVal1 := model.VoteKey{Height: 5, Round: 0, VoteType: "prevote", OriginNode: "val1", OriginShortAddr: "VAL1"}
	// Self-receipt for val1.
	require.Equal(t, "ok", prop.Matrix[keyVal1]["val1"].Status)
	require.Equal(t, time.Duration(0), prop.Matrix[keyVal1]["val1"].Latency)

	// val2 received val1's prevote at +100ms (first BA transition showing slot 0).
	require.Equal(t, "ok", prop.Matrix[keyVal1]["val2"].Status)
	require.Equal(t, 100*time.Millisecond, prop.Matrix[keyVal1]["val2"].Latency)

	// val3 received val1's prevote at +300ms.
	require.Equal(t, "ok", prop.Matrix[keyVal1]["val3"].Status)
	require.Equal(t, 300*time.Millisecond, prop.Matrix[keyVal1]["val3"].Latency)
}

func TestBuildPropagationSuppressesSurplusPrecommitMissAfterMaj23(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
				{Name: "val3"},
				{Name: "val4"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
			{Node: "val4", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val4",
			Height:       8,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_cast_at":   base,
			},
		},
		{
			Node:         "val1",
			Height:       8,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(150 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "VAL2",
				"_vrecv":       3,
				"_vtotal":      4,
				"_vmaj23":      true,
				"_vbits":       "xxx_",
			},
		},
	}

	prop := BuildPropagation(events, 8, resolver, []string{"val1", "val2", "val3", "val4"}, true)

	key := model.VoteKey{Height: 8, Round: 0, VoteType: "precommit", OriginNode: "val4", OriginShortAddr: "VAL4"}
	require.Equal(t, "quorum-satisfied", prop.Matrix[key]["val1"].Status)
	require.Equal(t, "ok", prop.Matrix[key]["val4"].Status)
}

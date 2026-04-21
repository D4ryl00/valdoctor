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
			Timestamp:    base.Add(time.Second + 500*time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vrecv":     2,
				"_vtotal":    3,
				"_vmaj23":    true,
				"_vbits":     "xx_",
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

func TestBuildPropagationTreatsEqualReceiptAndQuorumTimestampsAsSimultaneous(t *testing.T) {
	resolver := &IdentityResolver{
		Sources: []model.Source{
			{Node: "validator-a", Role: model.RoleValidator},
			{Node: "validator-b", Role: model.RoleValidator},
		},
		Metadata: model.Metadata{
			Nodes: map[string]model.MetadataNode{
				"validator-a": {ValidatorAddress: "AAAAAAAAAAAA0000000000000000000000000000"},
				"validator-b": {ValidatorAddress: "BBBBBBBBBBBB0000000000000000000000000000"},
			},
		},
	}

	base := time.Date(2026, 4, 21, 9, 30, 16, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "validator-a",
			Height:       42,
			Round:        0,
			Kind:         model.EventSignedVote,
			Timestamp:    base,
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "AAAAAAAAAAAA",
				"_cast_at":     base,
			},
		},
		{
			Node:         "validator-b",
			Height:       42,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vmaj23":    true,
				"_vbits":     "xx",
			},
		},
		{
			Node:         "validator-b",
			Height:       42,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "AAAAAAAAAAAA",
			},
		},
	}

	prop := BuildPropagation(events, 42, resolver, []string{"validator-a", "validator-b"}, true)
	key := model.VoteKey{
		Height:          42,
		Round:           0,
		VoteType:        "precommit",
		OriginNode:      "validator-a",
		OriginShortAddr: "AAAAAAAAAAAA",
	}

	require.Equal(t, "quorum-satisfied", prop.Matrix[key]["validator-b"].Status)
	require.Equal(t, 824*time.Millisecond, prop.Matrix[key]["validator-b"].Latency)
}

func TestBuildPropagationUsesBitArrayInclusionAtQuorumAsOnTime(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 9, 30, 16, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val1",
			Height:       43,
			Round:        0,
			Kind:         model.EventSignedVote,
			Timestamp:    base,
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_cast_at":   base,
				"_vidx":      0,
			},
		},
		{
			Node:         "val2",
			Height:       43,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vmaj23":    true,
				"_vbits":     "xx",
			},
		},
		{
			Node:         "val2",
			Height:       43,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "VAL1",
				"_vidx":        0,
			},
		},
	}

	prop := BuildPropagation(events, 43, resolver, []string{"val1", "val2"}, true)
	key := model.VoteKey{Height: 43, Round: 0, VoteType: "precommit", OriginNode: "val1", OriginShortAddr: "VAL1"}
	require.Equal(t, "ok", prop.Matrix[key]["val2"].Status)
	require.Equal(t, 824*time.Millisecond, prop.Matrix[key]["val2"].Latency)
}

func TestBuildPropagationResolvesRawVoteEnvelopePrefixViaValidatorIndex(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val2"},
				{Name: "val3"},
				{Name: "val1"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 9, 56, 44, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val2",
			Height:       47,
			Round:        0,
			Kind:         model.EventObservedPrevote,
			Timestamp:    base,
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vidx":        2,
				"_vaddrprefix": "CA3455817E50",
			},
		},
	}

	prop := BuildPropagation(events, 47, resolver, []string{"val1", "val2", "val3"}, true)
	key := model.VoteKey{
		Height:          47,
		Round:           0,
		VoteType:        "prevote",
		OriginNode:      "val1",
		OriginShortAddr: "",
	}

	_, ok := prop.Matrix[key]
	require.True(t, ok)
}

func TestBuildPropagationSkipsUnresolvedRawVoteEnvelopeOrigins(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 9, 56, 44, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val2",
			Height:       47,
			Round:        0,
			Kind:         model.EventObservedPrevote,
			Timestamp:    base,
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vaddrprefix": "DEADBEEF0000",
			},
		},
	}

	prop := BuildPropagation(events, 47, resolver, []string{"val1", "val2"}, true)
	require.Empty(t, prop.Matrix)
}

func TestBuildPropagationUsesBitArrayOrderToMarkAfterQuorumAsLate(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 9, 30, 16, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val1",
			Height:       44,
			Round:        0,
			Kind:         model.EventSignedVote,
			Timestamp:    base,
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_cast_at":   base,
				"_vidx":      0,
			},
		},
		{
			Node:         "val2",
			Height:       44,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vmaj23":    true,
				"_vbits":     "_x",
			},
		},
		{
			Node:         "val2",
			Height:       44,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vmaj23":    true,
				"_vbits":     "xx",
			},
		},
		{
			Node:         "val2",
			Height:       44,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			Timestamp:    base.Add(824 * time.Millisecond),
			HasTimestamp: true,
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vaddrprefix": "VAL1",
				"_vidx":        0,
			},
		},
	}

	prop := BuildPropagation(events, 44, resolver, []string{"val1", "val2"}, true)
	key := model.VoteKey{Height: 44, Round: 0, VoteType: "precommit", OriginNode: "val1", OriginShortAddr: "VAL1"}
	require.Equal(t, "late", prop.Matrix[key]["val2"].Status)
	require.Equal(t, 824*time.Millisecond, prop.Matrix[key]["val2"].Latency)
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

func TestBuildPropagationSuppressesPrevoteMissAfterMaj23(t *testing.T) {
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

	base := time.Date(2026, 4, 20, 16, 35, 5, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val4",
			Height:       215,
			Round:        1,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base.Add(4 * time.Second),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base.Add(4 * time.Second),
			},
		},
		{
			Node:         "val1",
			Height:       215,
			Round:        1,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(4*time.Second + 200*time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vaddrprefix": "VAL2",
				"_vbits":       "xxx_",
				"_vrecv":       3,
				"_vtotal":      4,
				"_vmaj23":      true,
			},
		},
	}

	prop := BuildPropagation(events, 215, resolver, []string{"val1", "val2", "val3", "val4"}, true)

	key := model.VoteKey{Height: 215, Round: 1, VoteType: "prevote", OriginNode: "val4", OriginShortAddr: "VAL4"}
	require.Equal(t, "quorum-satisfied", prop.Matrix[key]["val1"].Status)
	require.Equal(t, "ok", prop.Matrix[key]["val4"].Status)
}

func TestBuildPropagationSuppressesLatePrevoteReceiptAfterMaj23(t *testing.T) {
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

	base := time.Date(2026, 4, 20, 16, 35, 5, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val4",
			Height:       215,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base,
			},
		},
		{
			Node:         "val1",
			Height:       215,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(800 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vbits":     "xxx_",
				"_vrecv":     3,
				"_vtotal":    4,
				"_vmaj23":    true,
			},
		},
		{
			Node:         "val1",
			Height:       215,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(1300 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vbits":     "xxxx",
				"_vrecv":     4,
				"_vtotal":    4,
				"_vmaj23":    true,
			},
		},
	}

	prop := BuildPropagation(events, 215, resolver, []string{"val1", "val2", "val3", "val4"}, true)

	key := model.VoteKey{Height: 215, Round: 0, VoteType: "prevote", OriginNode: "val4", OriginShortAddr: "VAL4"}
	require.Equal(t, "late", prop.Matrix[key]["val1"].Status)
	require.Equal(t, 1300*time.Millisecond, prop.Matrix[key]["val1"].Latency)
	require.Equal(t, "ok", prop.Matrix[key]["val4"].Status)
}

func TestBuildPropagationMergesReceiveVotesBySignedVoteIndex(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 20, 21, 43, 13, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val1",
			Height:       160,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base,
				"_vidx":      0,
			},
		},
		{
			Node:         "val2",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(100 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vidx":        0,
				"_vaddrprefix": "0C0D31644548",
			},
		},
	}

	prop := BuildPropagation(events, 160, resolver, []string{"val1", "val2"}, true)

	key := model.VoteKey{Height: 160, Round: 0, VoteType: "prevote", OriginNode: "val1", OriginShortAddr: "VAL1"}
	require.Contains(t, prop.Matrix, key)
	require.Equal(t, "ok", prop.Matrix[key]["val1"].Status)
	require.Equal(t, "ok", prop.Matrix[key]["val2"].Status)
	for candidate := range prop.Matrix {
		require.NotEqual(t, "0C0D31644548", candidate.OriginNode)
	}
}

func TestBuildPropagationBitArrayPrefersSignedVoteRuntimeIndex(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
				{Name: "val3"},
				{Name: "val4"},
				{Name: "val5"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
			{Node: "val4", Role: model.RoleValidator},
			{Node: "val5", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 20, 21, 43, 23, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val5",
			Height:       160,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base,
				"_vidx":      3,
			},
		},
		{
			Node:         "val4",
			Height:       160,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base,
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_cast_at":   base,
				"_vidx":      4,
			},
		},
		{
			Node:         "val3",
			Height:       160,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(150 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vbits":     "___xx",
				"_vrecv":     2,
				"_vtotal":    5,
				"_vmaj23":    false,
			},
		},
	}

	prop := BuildPropagation(events, 160, resolver, []string{"val1", "val2", "val3", "val4", "val5"}, true)

	keyVal5 := model.VoteKey{Height: 160, Round: 0, VoteType: "prevote", OriginNode: "val5", OriginShortAddr: "VAL5"}
	keyVal4 := model.VoteKey{Height: 160, Round: 0, VoteType: "prevote", OriginNode: "val4", OriginShortAddr: "VAL4"}
	require.Equal(t, "ok", prop.Matrix[keyVal5]["val3"].Status)
	require.Equal(t, 150*time.Millisecond, prop.Matrix[keyVal5]["val3"].Latency)
	require.Equal(t, "ok", prop.Matrix[keyVal4]["val3"].Status)
	require.Equal(t, 150*time.Millisecond, prop.Matrix[keyVal4]["val3"].Latency)
}

func TestBuildPropagationTreatsReceivedPrecommitAfterMaj23AsLateNotMissing(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val4"},
				{Name: "val3"},
				{Name: "val5"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
			{Node: "val4", Role: model.RoleValidator},
			{Node: "val5", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 0, 2, 23, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val4",
			Height:       1342,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base.Add(514 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_cast_at":     base.Add(514 * time.Millisecond),
				"_vidx":        1,
				"_vaddrprefix": "0D827F14975A",
			},
		},
		{
			Node:         "val5",
			Height:       1342,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(616 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vrecv":     3,
				"_vtotal":    5,
				"_vmaj23":    false,
				"_vbits":     "x_xx_",
			},
		},
		{
			Node:         "val5",
			Height:       1342,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(618 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_vidx":        1,
				"_vaddrprefix": "0D827F14975A",
			},
		},
		{
			Node:         "val5",
			Height:       1342,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(618 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "precommit",
				"_vrecv":     4,
				"_vtotal":    5,
				"_vmaj23":    true,
				"_vbits":     "x_xxx",
			},
		},
	}

	prop := BuildPropagation(events, 1342, resolver, []string{"val1", "val2", "val3", "val4", "val5"}, true)

	var keyVal4 model.VoteKey
	found := false
	for candidate := range prop.Matrix {
		if candidate.Height == 1342 && candidate.Round == 0 && candidate.VoteType == "precommit" && candidate.OriginNode == "val4" {
			keyVal4 = candidate
			found = true
			break
		}
	}
	require.True(t, found)
	require.Equal(t, "quorum-satisfied", prop.Matrix[keyVal4]["val5"].Status)
	require.Equal(t, 104*time.Millisecond, prop.Matrix[keyVal4]["val5"].Latency)
}

func TestBuildPropagationSuppressesMissWhenReceiverHasLastPrecommitsSnapshot(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val4"},
				{Name: "val3"},
				{Name: "val5"},
				{Name: "val2"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
			{Node: "val4", Role: model.RoleValidator},
			{Node: "val5", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 0, 2, 23, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val4",
			Height:       1342,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base.Add(514 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "precommit",
				"_cast_at":     base.Add(514 * time.Millisecond),
				"_vidx":        1,
				"_vaddrprefix": "0D827F14975A",
			},
		},
		{
			Node:         "val5",
			Height:       1342,
			Round:        0,
			Kind:         model.EventAddedPrecommit,
			HasTimestamp: true,
			Timestamp:    base.Add(627 * time.Millisecond),
			Fields: map[string]any{
				"_vote_type":  "precommit",
				"_vrecv":      5,
				"_vtotal":     5,
				"_vmaj23":     true,
				"_vmaj23hash": "6B8F4DF0A6060AFD632900CC998BBADA59D12BF215772AD137B909D75E3D6C14",
				"_vbits":      "xxxxx",
			},
		},
	}

	prop := BuildPropagation(events, 1342, resolver, []string{"val1", "val2", "val3", "val4", "val5"}, true)

	var keyVal4 model.VoteKey
	found := false
	for candidate := range prop.Matrix {
		if candidate.Height == 1342 && candidate.Round == 0 && candidate.VoteType == "precommit" && candidate.OriginNode == "val4" {
			keyVal4 = candidate
			found = true
			break
		}
	}
	require.True(t, found)
	require.Equal(t, "ok", prop.Matrix[keyVal4]["val5"].Status)
	require.Equal(t, 113*time.Millisecond, prop.Matrix[keyVal4]["val5"].Latency)
}

func TestBuildPropagationUsesEarlierObservedVoteEvidenceBeforeDuplicateReceive(t *testing.T) {
	resolver := &IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "val1"},
				{Name: "val2"},
				{Name: "val3"},
				{Name: "val4"},
				{Name: "val5"},
			},
		},
		Sources: []model.Source{
			{Node: "val1", Role: model.RoleValidator},
			{Node: "val2", Role: model.RoleValidator},
			{Node: "val3", Role: model.RoleValidator},
			{Node: "val4", Role: model.RoleValidator},
			{Node: "val5", Role: model.RoleValidator},
		},
	}

	base := time.Date(2026, 4, 21, 9, 8, 13, 0, time.UTC)
	events := []model.Event{
		{
			Node:         "val5",
			Height:       1118,
			Round:        0,
			Kind:         model.EventSignedVote,
			HasTimestamp: true,
			Timestamp:    base.Add(964700885 * time.Nanosecond),
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_cast_at":     base.Add(964700885 * time.Nanosecond),
				"_vidx":        4,
				"_vaddrprefix": "C4F822B35F6C",
			},
		},
		{
			Node:         "val2",
			Height:       1118,
			Round:        0,
			Kind:         model.EventObservedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(time.Second + 73*time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vidx":      4,
			},
		},
		{
			Node:         "val2",
			Height:       1118,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(time.Second + 79*time.Millisecond),
			Fields: map[string]any{
				"_vote_type": "prevote",
				"_vrecv":     4,
				"_vtotal":    5,
				"_vmaj23":    true,
				"_vbits":     "xxx_x",
			},
		},
		{
			Node:         "val2",
			Height:       1118,
			Round:        0,
			Kind:         model.EventAddedPrevote,
			HasTimestamp: true,
			Timestamp:    base.Add(time.Second + 177*time.Millisecond),
			Fields: map[string]any{
				"_vote_type":   "prevote",
				"_vidx":        4,
				"_vaddrprefix": "C4F822B35F6C",
			},
		},
	}

	prop := BuildPropagation(events, 1118, resolver, []string{"val1", "val2", "val3", "val4", "val5"}, true)

	var key model.VoteKey
	found := false
	for candidate := range prop.Matrix {
		if candidate.Height == 1118 && candidate.Round == 0 && candidate.VoteType == "prevote" && candidate.OriginNode == "val5" {
			key = candidate
			found = true
			break
		}
	}
	require.True(t, found)
	require.Equal(t, "ok", prop.Matrix[key]["val2"].Status)
	require.Equal(t, 108299115*time.Nanosecond, prop.Matrix[key]["val2"].Latency)
}

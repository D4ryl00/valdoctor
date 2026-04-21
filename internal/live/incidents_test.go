package live

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

func TestDetectActiveIncidentsSingleReceiverMissNamesVoteType(t *testing.T) {
	now := time.Now().UTC()
	heights := []model.HeightEntry{{
		Height: 10,
		Propagation: model.VotePropagation{
			Height: 10,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 10, Round: 0, VoteType: "prevote", OriginNode: "val3"}: {
					"val2": {Status: "missing"},
				},
			},
		},
	}}

	active := detectActiveIncidents(10, nil, heights, nil, now)

	card, ok := active["vote-propagation-miss-val3-val2-prevote"]
	require.True(t, ok)
	require.Equal(t, "vote-propagation-miss", card.Kind)
	require.Equal(t, model.SeverityLow, card.Severity)
	require.Equal(t, "Missing prevote receipts from val3 to val2", card.Title)
	require.Contains(t, card.Summary, "prevote")
	require.Equal(t, "val2", card.Scope)
}

func TestDetectActiveIncidentsMultiReceiverMissBecomesBroadcastIssue(t *testing.T) {
	now := time.Now().UTC()
	heights := []model.HeightEntry{{
		Height: 10,
		Propagation: model.VotePropagation{
			Height: 10,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 10, Round: 1, VoteType: "precommit", OriginNode: "val3"}: {
					"val1": {Status: "missing"},
					"val2": {Status: "missing"},
				},
			},
		},
	}}

	active := detectActiveIncidents(10, nil, heights, nil, now)

	card, ok := active["vote-propagation-miss-multi-val3-precommit-h10-r1"]
	require.True(t, ok)
	require.Equal(t, "vote-propagation-miss-broadcast", card.Kind)
	require.Equal(t, model.SeverityMedium, card.Severity)
	require.Equal(t, "Missing precommit receipts from val3 to val1 and val2", card.Title)
	require.Equal(t, "h10/r1", card.Scope)

	_, singleVal1 := active["vote-propagation-miss-val3-val1-precommit"]
	_, singleVal2 := active["vote-propagation-miss-val3-val2-precommit"]
	require.False(t, singleVal1)
	require.False(t, singleVal2)
}

func TestDetectActiveIncidentsIgnoresOldPropagationMisses(t *testing.T) {
	now := time.Now().UTC()
	heights := []model.HeightEntry{{
		Height: 10,
		Propagation: model.VotePropagation{
			Height: 10,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 10, Round: 0, VoteType: "precommit", OriginNode: "val1"}: {
					"val2": {Status: "missing"},
				},
			},
		},
	}}

	active := detectActiveIncidents(20, nil, heights, nil, now)

	require.Empty(t, active)
}

func TestDetectActiveIncidentsIgnoresQuorumSatisfiedPrecommitGaps(t *testing.T) {
	now := time.Now().UTC()
	heights := []model.HeightEntry{{
		Height: 10,
		Propagation: model.VotePropagation{
			Height: 10,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 10, Round: 0, VoteType: "precommit", OriginNode: "val4"}: {
					"val1": {Status: "quorum-satisfied"},
				},
			},
		},
	}}

	active := detectActiveIncidents(10, nil, heights, nil, now)

	require.Empty(t, active)
}

func TestDetectActiveIncidentsKeepsRecentRemoteSignerInstabilityActive(t *testing.T) {
	now := time.Now().UTC()
	nodes := []model.NodeState{{
		Summary: model.NodeSummary{
			Name:          "val4",
			Role:          model.RoleValidator,
			HighestCommit: 42,
			LastHeight:    42,
			End:           now,
			AvgBlockTime:  2 * time.Second,
		},
	}}
	events := []model.Event{
		{
			Node:         "val4",
			Kind:         model.EventRemoteSignerFailure,
			Height:       42,
			HasTimestamp: true,
			Timestamp:    now.Add(-4 * time.Second),
		},
		{
			Node:         "val4",
			Kind:         model.EventRemoteSignerConnect,
			Height:       42,
			HasTimestamp: true,
			Timestamp:    now.Add(-3 * time.Second),
		},
	}

	active := detectActiveIncidents(42, events, nil, nodes, now)

	card, ok := active["remote-signer-instability-val4"]
	require.True(t, ok)
	require.Equal(t, "remote-signer-instability", card.Kind)
	require.Equal(t, int64(42), card.FirstHeight)
	require.Equal(t, int64(42), card.LastHeight)
	require.Contains(t, card.Summary, "observed recently")
}

func TestDetectActiveIncidentsResolvesOldRemoteSignerInstability(t *testing.T) {
	now := time.Now().UTC()
	nodes := []model.NodeState{{
		Summary: model.NodeSummary{
			Name:          "val5",
			Role:          model.RoleValidator,
			HighestCommit: 55,
			LastHeight:    55,
			End:           now,
			AvgBlockTime:  2 * time.Second,
		},
	}}
	events := []model.Event{
		{
			Node:         "val5",
			Kind:         model.EventRemoteSignerFailure,
			Height:       44,
			HasTimestamp: true,
			Timestamp:    now.Add(-20 * time.Second),
		},
		{
			Node:         "val5",
			Kind:         model.EventRemoteSignerConnect,
			Height:       44,
			HasTimestamp: true,
			Timestamp:    now.Add(-19 * time.Second),
		},
	}

	active := detectActiveIncidents(55, events, nil, nodes, now)

	require.NotContains(t, active, "remote-signer-instability-val5")
}

func TestDetectActiveIncidentsFlagsMissingPrevoteOnHaltedTip(t *testing.T) {
	now := time.Now().UTC()
	nodes := []model.NodeState{
		{
			Summary: model.NodeSummary{Name: "val1", Role: model.RoleValidator, HighestCommit: 46, StallDuration: 20 * time.Second},
		},
		{
			Summary: model.NodeSummary{Name: "val2", Role: model.RoleValidator, HighestCommit: 46, StallDuration: 20 * time.Second},
		},
		{
			Summary: model.NodeSummary{Name: "val3", Role: model.RoleValidator, HighestCommit: 46, StallDuration: 20 * time.Second},
		},
	}
	heights := []model.HeightEntry{{
		Height: 47,
		Report: model.HeightReport{
			Height:         47,
			CommittedInLog: false,
			Rounds: []model.RoundSummary{{
				Round:         0,
				PrevotesTotal: 2,
				PrevotesMaj23: false,
			}},
			ValidatorVotes: []model.ValidatorVoteRow{
				{
					Name: "val2",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteBlock, Precommit: model.VoteAbsent},
					},
				},
				{
					Name: "val3",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteBlock, Precommit: model.VoteAbsent},
					},
				},
				{
					Name: "val1",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteAbsent, Precommit: model.VoteAbsent},
					},
				},
			},
		},
	}}

	active := detectActiveIncidents(47, nil, heights, nodes, now)

	card, ok := active["missing-cast-prevote-val1-h47-r0"]
	require.True(t, ok)
	require.Equal(t, "missing-cast-prevote", card.Kind)
	require.Equal(t, model.SeverityHigh, card.Severity)
	require.Equal(t, "val1 did not prevote at h47/r0", card.Title)
	require.Contains(t, card.Summary, "absent")
}

func TestDetectActiveIncidentsFlagsMissingPrecommitOnHaltedTip(t *testing.T) {
	now := time.Now().UTC()
	nodes := []model.NodeState{
		{
			Summary: model.NodeSummary{Name: "val1", Role: model.RoleValidator, HighestCommit: 51, StallDuration: 20 * time.Second},
		},
		{
			Summary: model.NodeSummary{Name: "val2", Role: model.RoleValidator, HighestCommit: 51, StallDuration: 20 * time.Second},
		},
		{
			Summary: model.NodeSummary{Name: "val3", Role: model.RoleValidator, HighestCommit: 51, StallDuration: 20 * time.Second},
		},
	}
	heights := []model.HeightEntry{{
		Height: 52,
		Report: model.HeightReport{
			Height:         52,
			CommittedInLog: false,
			Rounds: []model.RoundSummary{{
				Round:             0,
				PrevotesTotal:     3,
				PrevotesMaj23:     true,
				PrecommitDataSeen: true,
				PrecommitsTotal:   2,
				PrecommitsMaj23:   false,
			}},
			ValidatorVotes: []model.ValidatorVoteRow{
				{
					Name: "val1",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteBlock, Precommit: model.VoteAbsent},
					},
				},
				{
					Name: "val2",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteBlock, Precommit: model.VoteBlock},
					},
				},
				{
					Name: "val3",
					ByRound: map[int]model.VoteEntry{
						0: {Prevote: model.VoteBlock, Precommit: model.VoteBlock},
					},
				},
			},
		},
	}}

	active := detectActiveIncidents(52, nil, heights, nodes, now)

	card, ok := active["missing-cast-precommit-val1-h52-r0"]
	require.True(t, ok)
	require.Equal(t, "missing-cast-precommit", card.Kind)
	require.Equal(t, "val1 did not precommit at h52/r0", card.Title)
}

func TestIncidentEngineReactivatedPropagationMissStartsNewEpisode(t *testing.T) {
	now := time.Now().UTC()
	engine := &IncidentEngine{}
	mem := storepkg.NewMemoryStore(8)

	first := engine.Reconcile(mem, 152, nil, []model.HeightEntry{{
		Height: 152,
		Propagation: model.VotePropagation{
			Height: 152,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 152, Round: 0, VoteType: "precommit", OriginNode: "val4"}: {
					"val5": {Status: "missing"},
				},
			},
		},
	}}, nil, now)
	require.Len(t, first, 1)
	require.Equal(t, int64(152), first[0].FirstHeight)
	require.Equal(t, int64(152), first[0].LastHeight)

	resolved := engine.Reconcile(mem, 155, nil, nil, nil, now.Add(time.Second))
	require.Len(t, resolved, 1)
	require.Equal(t, "resolved", resolved[0].Status)

	second := engine.Reconcile(mem, 1342, nil, []model.HeightEntry{{
		Height: 1342,
		Propagation: model.VotePropagation{
			Height: 1342,
			Matrix: map[model.VoteKey]map[string]*model.VoteReceipt{
				{Height: 1342, Round: 0, VoteType: "precommit", OriginNode: "val4"}: {
					"val5": {Status: "missing"},
				},
			},
		},
	}}, nil, now.Add(2*time.Second))
	require.Len(t, second, 1)
	require.Equal(t, "active", second[0].Status)
	require.Equal(t, int64(1342), second[0].FirstHeight)
	require.Equal(t, int64(1342), second[0].LastHeight)
}

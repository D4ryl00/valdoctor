package live

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/D4ryl00/valdoctor/internal/model"
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

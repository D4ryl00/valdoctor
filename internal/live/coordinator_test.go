package live

import (
	"context"
	"testing"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/source"
	"github.com/D4ryl00/valdoctor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorBuildsHeightsAndPrintsProgress(t *testing.T) {
	src := &stubSource{
		lines: []source.Line{
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Finalizing commit of block","height":10}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":11}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
		},
	}

	memStore := store.NewMemoryStore(5)
	var tips []int64
	var closed []int64

	coord := &Coordinator{
		Source:   src,
		Store:    memStore,
		Genesis:  model.Genesis{ChainID: "test-chain"},
		Sources:  []model.Source{{Path: "/tmp/validator-a.log", Node: "validator-a", Role: model.RoleValidator}},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"validator-a"},
		},
		MaxHistory: 5,
		OnTipAdvanced: func(height int64) {
			tips = append(tips, height)
		},
		OnHeightClosed: func(height int64) {
			closed = append(closed, height)
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		_, ok10 := memStore.GetHeight(10)
		_, ok11 := memStore.GetHeight(11)
		return ok10 && ok11
	}, time.Second, 10*time.Millisecond)

	entry, ok := memStore.GetHeight(11)
	require.True(t, ok)
	require.Equal(t, model.HeightClosed, entry.Status)
	require.EqualValues(t, 11, memStore.CurrentTip())
	require.Equal(t, []int64{10, 11}, tips)
	require.Equal(t, []int64{10, 11}, closed)

	nodes := memStore.NodeStates()
	require.Len(t, nodes, 1)
	require.Equal(t, "validator-a", nodes[0].Summary.Name)
	require.EqualValues(t, 11, nodes[0].Summary.HighestCommit)
}

func TestCoordinatorEmitsPropagationIncidentAfterGrace(t *testing.T) {
	src := &stubSource{
		lines: []source.Line{
			{
				Raw:    `{"level":"info","ts":1776333600,"msg":"Signed and pushed vote","height":10,"round":0,"type":1,"timestamp":"2026-04-15 10:00:00 +0000 UTC","validator address":"AAAAAAAAAAAA0000000000000000000000000000"}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 1,
			},
			{
				Raw:    `{"level":"info","ts":1776333601,"msg":"Finalizing commit of block","height":10}`,
				Path:   "/tmp/validator-a.log",
				Node:   "validator-a",
				Role:   model.RoleValidator,
				LineNo: 2,
			},
		},
	}

	memStore := store.NewMemoryStore(5)
	updates := make(chan model.IncidentCard, 8)

	coord := &Coordinator{
		Source:  src,
		Store:   memStore,
		Genesis: model.Genesis{ChainID: "test-chain"},
		Sources: []model.Source{
			{Path: "/tmp/validator-a.log", Node: "validator-a", Role: model.RoleValidator},
			{Path: "/tmp/validator-b.log", Node: "validator-b", Role: model.RoleValidator},
		},
		Debounce: 5 * time.Millisecond,
		ClosureEvaluator: ClosureEvaluator{
			Policy:           model.PolicySingleValidatorCommit,
			ValidatorSources: []string{"validator-a", "validator-b"},
			GraceWindow:      20 * time.Millisecond,
		},
		MaxHistory: 5,
		OnIncidentUpdate: func(card model.IncidentCard) {
			updates <- card
		},
	}

	require.NoError(t, coord.Run(context.Background()))

	require.Eventually(t, func() bool {
		for _, card := range memStore.ActiveIncidents() {
			if card.Kind == "vote-propagation-miss" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)

	select {
	case update := <-updates:
		require.Equal(t, "vote-propagation-miss", update.Kind)
		require.Equal(t, "active", update.Status)
		require.Equal(t, "vote-propagation-miss-validator-a-validator-b", update.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incident update")
	}
}

type stubSource struct {
	lines []source.Line
}

func (s *stubSource) Name() string {
	return "stub"
}

func (s *stubSource) Stream(ctx context.Context) (<-chan source.Line, <-chan error) {
	lines := make(chan source.Line, len(s.lines))
	errs := make(chan error)

	for _, line := range s.lines {
		lines <- line
	}
	close(lines)
	close(errs)

	return lines, errs
}

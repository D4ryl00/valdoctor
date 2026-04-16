package live

import (
	"fmt"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

type IncidentEngine struct {
	cards map[string]model.IncidentCard
}

func (e *IncidentEngine) Reconcile(
	store storepkg.Store,
	tip int64,
	events []model.Event,
	heights []model.HeightEntry,
	nodes []model.NodeState,
	now time.Time,
) []model.IncidentCard {
	if e.cards == nil {
		e.cards = map[string]model.IncidentCard{}
	}

	active := detectActiveIncidents(tip, events, heights, nodes, now)
	updates := make([]model.IncidentCard, 0)

	for id, next := range active {
		prev, ok := e.cards[id]
		if ok {
			next.FirstHeight = prev.FirstHeight
			if next.FirstHeight == 0 {
				next.FirstHeight = next.LastHeight
			}
			if next.LastHeight < prev.LastHeight {
				next.LastHeight = prev.LastHeight
			}
			if prev.Status == "resolved" {
				next.FirstHeight = prev.FirstHeight
			}
			if cardsEqual(prev, next) {
				e.cards[id] = next
				continue
			}
		} else if next.FirstHeight == 0 {
			next.FirstHeight = next.LastHeight
		}

		e.cards[id] = next
		store.UpsertIncident(next)
		updates = append(updates, next)
	}

	for id, prev := range e.cards {
		if prev.Status != "active" {
			continue
		}
		if _, ok := active[id]; ok {
			continue
		}

		resolved := prev
		resolved.Status = "resolved"
		resolved.UpdatedAt = now
		e.cards[id] = resolved
		store.UpsertIncident(resolved)
		updates = append(updates, resolved)
	}

	return updates
}

func detectActiveIncidents(
	tip int64,
	events []model.Event,
	heights []model.HeightEntry,
	nodes []model.NodeState,
	now time.Time,
) map[string]model.IncidentCard {
	active := map[string]model.IncidentCard{}

	for _, event := range events {
		if event.Kind != model.EventConsensusFailure {
			continue
		}
		id := "consensus-panic-" + event.Node
		active[id] = model.IncidentCard{
			ID:          id,
			Kind:        "consensus-panic",
			Severity:    model.SeverityCritical,
			Status:      "active",
			Scope:       event.Node,
			Title:       fmt.Sprintf("Consensus panic on %s", event.Node),
			Summary:     "A CONSENSUS FAILURE!!! panic was logged.",
			FirstHeight: event.Height,
			LastHeight:  event.Height,
			UpdatedAt:   now,
			Evidence: []model.Evidence{{
				Node:      event.Node,
				Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
				Path:      event.Path,
				Line:      event.Line,
				Message:   event.Message,
			}},
		}
	}

	for _, node := range nodes {
		summary := node.Summary

		if summary.MaxRoundSeen >= 3 {
			id := "round-escalation-" + summary.Name
			active[id] = model.IncidentCard{
				ID:          id,
				Kind:        "round-escalation",
				Severity:    model.SeverityMedium,
				Status:      "active",
				Scope:       summary.Name,
				Title:       fmt.Sprintf("%s reached round %d", summary.Name, summary.MaxRoundSeen),
				Summary:     fmt.Sprintf("Consensus reached round %d at height %d.", summary.MaxRoundSeen, summary.MaxRoundHeight),
				FirstHeight: summary.MaxRoundHeight,
				LastHeight:  summary.MaxRoundHeight,
				UpdatedAt:   now,
			}
		}

		if summary.Role == model.RoleValidator && tip > summary.HighestCommit && summary.StallDuration >= stallThreshold(summary) {
			id := "stalled-validator-" + summary.Name
			active[id] = model.IncidentCard{
				ID:          id,
				Kind:        "stalled-validator",
				Severity:    model.SeverityHigh,
				Status:      "active",
				Scope:       summary.Name,
				Title:       fmt.Sprintf("%s is stalled below tip", summary.Name),
				Summary:     fmt.Sprintf("Tip is h%d while the node only committed through h%d.", tip, summary.HighestCommit),
				FirstHeight: summary.HighestCommit + 1,
				LastHeight:  tip,
				UpdatedAt:   now,
			}
		}

		if summary.SignerFailureCount >= 2 || (summary.SignerFailureCount >= 1 && summary.SignerConnectCount >= 1) {
			id := "remote-signer-instability-" + summary.Name
			active[id] = model.IncidentCard{
				ID:          id,
				Kind:        "remote-signer-instability",
				Severity:    model.SeverityHigh,
				Status:      "active",
				Scope:       summary.Name,
				Title:       fmt.Sprintf("Remote signer instability on %s", summary.Name),
				Summary:     fmt.Sprintf("%d signer failure(s) and %d reconnect(s) observed.", summary.SignerFailureCount, summary.SignerConnectCount),
				FirstHeight: summary.LastHeight,
				LastHeight:  summary.LastHeight,
				UpdatedAt:   now,
			}
		}
	}

	for _, height := range heights {
		for key, receivers := range height.Propagation.Matrix {
			for receiver, receipt := range receivers {
				switch receipt.Status {
				case "missing":
					id := fmt.Sprintf("vote-propagation-miss-%s-%s", key.OriginNode, receiver)
					active[id] = aggregatePropagationCard(active[id], now, id, "vote-propagation-miss", model.SeverityHigh, key, receiver, "missing")
				case "late":
					id := fmt.Sprintf("vote-propagation-late-%s-%s", key.OriginNode, receiver)
					active[id] = aggregatePropagationCard(active[id], now, id, "vote-propagation-late", model.SeverityMedium, key, receiver, "late")
				}
			}
		}
	}

	return active
}

func aggregatePropagationCard(existing model.IncidentCard, now time.Time, id, kind string, severity model.Severity, key model.VoteKey, receiver, status string) model.IncidentCard {
	card := model.IncidentCard{
		ID:          id,
		Kind:        kind,
		Severity:    severity,
		Status:      "active",
		Scope:       receiver,
		Title:       fmt.Sprintf("%s receipts from %s to %s", strings.Title(strings.ReplaceAll(status, "-", " ")), key.OriginNode, receiver),
		Summary:     fmt.Sprintf("%s vote receipts observed for %s %s at h%d/r%d.", status, key.OriginNode, key.VoteType, key.Height, key.Round),
		FirstHeight: key.Height,
		LastHeight:  key.Height,
		UpdatedAt:   now,
	}

	if existing.ID != "" {
		card.FirstHeight = existing.FirstHeight
		if card.FirstHeight == 0 {
			card.FirstHeight = key.Height
		}
		card.LastHeight = max(card.LastHeight, existing.LastHeight)
		card.Summary = fmt.Sprintf("%s receipts observed for %s %s on heights %d-%d.", status, key.OriginNode, key.VoteType, card.FirstHeight, card.LastHeight)
	}

	return card
}

func cardsEqual(a, b model.IncidentCard) bool {
	return a.ID == b.ID &&
		a.Kind == b.Kind &&
		a.Severity == b.Severity &&
		a.Status == b.Status &&
		a.Scope == b.Scope &&
		a.Title == b.Title &&
		a.Summary == b.Summary &&
		a.FirstHeight == b.FirstHeight &&
		a.LastHeight == b.LastHeight
}

func stallThreshold(summary model.NodeSummary) time.Duration {
	if summary.AvgBlockTime > 0 {
		threshold := summary.AvgBlockTime * 3
		if threshold < 10*time.Second {
			return 10 * time.Second
		}
		return threshold
	}
	return 15 * time.Second
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

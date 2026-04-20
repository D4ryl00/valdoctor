package live

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

type IncidentEngine struct {
	cards map[string]model.IncidentCard
}

const propagationIncidentActiveWindow = 2
const remoteSignerIncidentMinWindow = 10 * time.Second

type recentSignerStats struct {
	failures  int
	connects  int
	firstSeen time.Time
	lastSeen  time.Time
	firstH    int64
	lastH     int64
}

func remoteSignerIncidentWindow(avgBlock time.Duration) time.Duration {
	window := avgBlock * 5
	if window < remoteSignerIncidentMinWindow {
		window = remoteSignerIncidentMinWindow
	}
	return window
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
	summariesByNode := map[string]model.NodeSummary{}
	recentSignerByNode := map[string]recentSignerStats{}

	for _, node := range nodes {
		summariesByNode[node.Summary.Name] = node.Summary
	}

	for _, event := range events {
		switch event.Kind {
		case model.EventRemoteSignerFailure, model.EventRemoteSignerConnect:
			summary, ok := summariesByNode[event.Node]
			if !ok {
				break
			}
			if !event.HasTimestamp || summary.End.IsZero() {
				break
			}
			if age := summary.End.Sub(event.Timestamp); age < 0 || age > remoteSignerIncidentWindow(summary.AvgBlockTime) {
				break
			}
			stats := recentSignerByNode[event.Node]
			if stats.firstSeen.IsZero() || event.Timestamp.Before(stats.firstSeen) {
				stats.firstSeen = event.Timestamp
			}
			if event.Timestamp.After(stats.lastSeen) {
				stats.lastSeen = event.Timestamp
			}
			if event.Height > 0 {
				if stats.firstH == 0 || event.Height < stats.firstH {
					stats.firstH = event.Height
				}
				if event.Height > stats.lastH {
					stats.lastH = event.Height
				}
			}
			if event.Kind == model.EventRemoteSignerFailure {
				stats.failures++
			} else {
				stats.connects++
			}
			recentSignerByNode[event.Node] = stats
		}

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

		if summary.Role == model.RoleValidator && tip > summary.HighestCommit && summary.StallDuration >= summary.StallThreshold() {
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

		if stats := recentSignerByNode[summary.Name]; stats.failures >= 2 || (stats.failures >= 1 && stats.connects >= 1) {
			id := "remote-signer-instability-" + summary.Name
			firstHeight := stats.firstH
			lastHeight := stats.lastH
			if lastHeight == 0 {
				switch {
				case summary.LastHeight > 0:
					lastHeight = summary.LastHeight
				case summary.HighestCommit > 0:
					lastHeight = summary.HighestCommit
				default:
					lastHeight = tip
				}
			}
			if firstHeight == 0 {
				firstHeight = lastHeight
			}
			active[id] = model.IncidentCard{
				ID:          id,
				Kind:        "remote-signer-instability",
				Severity:    model.SeverityHigh,
				Status:      "active",
				Scope:       summary.Name,
				Title:       fmt.Sprintf("Remote signer instability on %s", summary.Name),
				Summary:     fmt.Sprintf("%d signer failure(s) and %d reconnect(s) observed recently.", stats.failures, stats.connects),
				FirstHeight: firstHeight,
				LastHeight:  lastHeight,
				UpdatedAt:   now,
			}
		}
	}

	for _, height := range heights {
		if tip-height.Height > propagationIncidentActiveWindow {
			continue
		}
		accumulatePropagationIncidents(active, height, nodes, tip, now)
	}

	return active
}

func accumulatePropagationIncidents(active map[string]model.IncidentCard, height model.HeightEntry, nodes []model.NodeState, tip int64, now time.Time) {
	receiverStates := map[string]model.NodeSummary{}
	validatorCount := 0
	for _, node := range nodes {
		receiverStates[node.Summary.Name] = node.Summary
		if node.Summary.Role == model.RoleValidator {
			validatorCount++
		}
	}

	for key, receivers := range height.Propagation.Matrix {
		missingReceivers := make([]string, 0)
		for receiver, receipt := range receivers {
			switch receipt.Status {
			case "missing":
				missingReceivers = append(missingReceivers, receiver)
			case "late":
				id := fmt.Sprintf("vote-propagation-late-%s-%s-%s", key.OriginNode, receiver, key.VoteType)
				active[id] = aggregatePropagationCard(active[id], now, id, "vote-propagation-late", model.SeverityMedium, key, receiver, "late")
			}
		}

		switch len(missingReceivers) {
		case 0:
			continue
		case 1:
			receiver := missingReceivers[0]
			id := fmt.Sprintf("vote-propagation-miss-%s-%s-%s", key.OriginNode, receiver, key.VoteType)
			active[id] = aggregatePropagationCard(
				active[id],
				now,
				id,
				"vote-propagation-miss",
				propagationMissSeverity(receiverStates[receiver], tip, false, len(missingReceivers), validatorCount),
				key,
				receiver,
				"missing",
			)
		default:
			sort.Strings(missingReceivers)
			id := fmt.Sprintf("vote-propagation-miss-multi-%s-%s-h%d-r%d", key.OriginNode, key.VoteType, key.Height, key.Round)
			active[id] = propagationBroadcastMissCard(now, id, key, missingReceivers, receiverStates, tip, validatorCount)
		}
	}
}

func aggregatePropagationCard(existing model.IncidentCard, now time.Time, id, kind string, severity model.Severity, key model.VoteKey, receiver, status string) model.IncidentCard {
	card := model.IncidentCard{
		ID:          id,
		Kind:        kind,
		Severity:    severity,
		Status:      "active",
		Scope:       receiver,
		Title:       fmt.Sprintf("%s %s receipts from %s to %s", strings.Title(strings.ReplaceAll(status, "-", " ")), key.VoteType, key.OriginNode, receiver),
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
		card.Summary = fmt.Sprintf("%s %s receipts observed for %s on heights %d-%d toward %s.", status, key.VoteType, key.OriginNode, card.FirstHeight, card.LastHeight, receiver)
	}

	return card
}

func propagationBroadcastMissCard(now time.Time, id string, key model.VoteKey, receivers []string, receiverStates map[string]model.NodeSummary, tip int64, validatorCount int) model.IncidentCard {
	label := fmt.Sprintf("%d validators", len(receivers))
	if len(receivers) == 2 {
		label = strings.Join(receivers, " and ")
	}

	severity := model.SeverityMedium
	for _, receiver := range receivers {
		if propagationMissSeverity(receiverStates[receiver], tip, true, len(receivers), validatorCount) == model.SeverityHigh {
			severity = model.SeverityHigh
			break
		}
	}

	return model.IncidentCard{
		ID:          id,
		Kind:        "vote-propagation-miss-broadcast",
		Severity:    severity,
		Status:      "active",
		Scope:       fmt.Sprintf("h%d/r%d", key.Height, key.Round),
		Title:       fmt.Sprintf("Missing %s receipts from %s to %s", key.VoteType, key.OriginNode, label),
		Summary:     fmt.Sprintf("%s did not log %s receipts on %s at h%d/r%d.", strings.Join(receivers, ", "), key.VoteType, key.OriginNode, key.Height, key.Round),
		FirstHeight: key.Height,
		LastHeight:  key.Height,
		UpdatedAt:   now,
	}
}

func propagationMissSeverity(receiver model.NodeSummary, tip int64, multi bool, impactedReceivers, validatorCount int) model.Severity {
	if receiver.Name != "" && receiver.Role == model.RoleValidator && receiver.HighestCommit < tip {
		threshold := 2
		if validatorCount/2 > threshold {
			threshold = validatorCount / 2
		}
		if multi && impactedReceivers >= threshold {
			return model.SeverityHigh
		}
		return model.SeverityHigh
	}
	if multi {
		return model.SeverityMedium
	}
	return model.SeverityLow
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

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

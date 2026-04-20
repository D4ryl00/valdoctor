package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/render"
)

func renderDetail(m Model) string {
	entry, ok := m.currentHeightEntry()
	if !ok {
		return m.styles.doc.Render("No retained height is selected.")
	}

	tabRow := []string{
		tabChip(m, tabConsensus, "Consensus"),
		tabChip(m, tabPropagation, "Propagation"),
	}

	title := m.styles.title.Render(fmt.Sprintf("Height h%d", entry.Height))
	meta := m.styles.muted.Render(fmt.Sprintf("status %s  ·  recent heights %s", heightStatusText(entry.Status), recentHeightList(m)))
	body := m.viewport.View()
	help := m.styles.help.Render(keyHelp(m.mode, m.searching, m.showHelp, m.confirmQuit))

	return m.styles.doc.Render(strings.Join([]string{
		title,
		meta,
		strings.Join(tabRow, " "),
		body,
		help,
	}, "\n\n"))
}

func renderConsensusContent(entry model.HeightEntry, color bool) string {
	return render.HeightText(entry.Report, color)
}

func renderPropagationContent(entry model.HeightEntry, nodes []model.NodeState) string {
	if len(entry.Propagation.Matrix) == 0 {
		return "No propagation data for this height yet."
	}

	// Build per-node lookups from the node state snapshot.
	genesisIdxOf := make(map[string]int, len(nodes))
	shortAddrOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		genesisIdxOf[n.Summary.Name] = n.Summary.GenesisIndex
		if addr := n.Summary.ShortAddr; len(addr) >= 6 {
			shortAddrOf[n.Summary.Name] = addr[:6]
		} else if addr != "" {
			shortAddrOf[n.Summary.Name] = addr
		}
	}

	receivers := make([]string, 0)
	seenReceivers := map[string]struct{}{}
	keys := make([]model.VoteKey, 0, len(entry.Propagation.Matrix))
	for key, row := range entry.Propagation.Matrix {
		keys = append(keys, key)
		for receiver := range row {
			if _, ok := seenReceivers[receiver]; ok {
				continue
			}
			seenReceivers[receiver] = struct{}{}
			receivers = append(receivers, receiver)
		}
	}
	sortByGenesisIndex(receivers, genesisIdxOf)
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].Round != keys[j].Round {
			return keys[i].Round < keys[j].Round
		}
		if keys[i].VoteType != keys[j].VoteType {
			return voteTypeOrder(keys[i].VoteType) < voteTypeOrder(keys[j].VoteType)
		}
		ii := genesisIdxOf[keys[i].OriginNode]
		ji := genesisIdxOf[keys[j].OriginNode]
		if ii != ji {
			switch {
			case ii >= 0 && ji >= 0:
				return ii < ji
			case ii >= 0:
				return true
			case ji >= 0:
				return false
			}
		}
		return keys[i].OriginNode < keys[j].OriginNode
	})

	var b strings.Builder
	b.WriteString("Vote propagation matrix\n")
	b.WriteString("Legend: ok | late | missing | ? = unknown_cast_time | - = no receipt log data | pending\n\n")

	// Format: name(g1gs04) — 6-char short address in parens.
	nodeLabel := func(name string) string {
		if addr := shortAddrOf[name]; addr != "" {
			return truncate(name, 14) + "(" + addr + ")"
		}
		return truncate(name, 14)
	}

	header := fmt.Sprintf("%-12s %-24s", "Vote", "Origin")
	for _, receiver := range receivers {
		// Truncate name to 6 chars in column headers to keep columns narrow.
		recvLabel := truncate(receiver, 6)
		if addr := shortAddrOf[receiver]; addr != "" {
			recvLabel += "(" + addr + ")"
		}
		header += fmt.Sprintf(" %-14s", recvLabel)
	}
	b.WriteString(header + "\n")
	b.WriteString(strings.Repeat("─", len(header)) + "\n")

	for _, key := range keys {
		row := entry.Propagation.Matrix[key]
		line := fmt.Sprintf("%-12s %-24s", fmt.Sprintf("r%d %s", key.Round, shortVoteType(key.VoteType)), nodeLabel(key.OriginNode))
		for _, receiver := range receivers {
			line += fmt.Sprintf(" %-14s", receiptCell(row[receiver]))
		}
		b.WriteString(line + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func recentHeightList(m Model) string {
	if len(m.snap.recentHeights) == 0 {
		return "none"
	}
	items := make([]string, 0, min(6, len(m.snap.recentHeights)))
	for i := 0; i < len(m.snap.recentHeights) && i < 6; i++ {
		height := m.snap.recentHeights[i].Height
		label := fmt.Sprintf("h%d", height)
		if height == m.selectedHeight {
			label = "[" + label + "]"
		}
		items = append(items, label)
	}
	return strings.Join(items, " ")
}

func tabChip(m Model, tab detailTab, label string) string {
	if m.detailTab == tab {
		return m.styles.activeTab.Render(label)
	}
	return m.styles.inactiveTab.Render(label)
}

func receiptCell(receipt *model.VoteReceipt) string {
	if receipt == nil {
		return "pending"
	}
	switch receipt.Status {
	case "ok":
		return fmt.Sprintf("ok %s", receipt.Latency.Round(timeDisplayPrecision(receipt.Latency)))
	case "late":
		return fmt.Sprintf("late %s", receipt.Latency.Round(timeDisplayPrecision(receipt.Latency)))
	case "missing":
		return "missing"
	case "unknown_cast_time":
		return "?"
	case "no-data":
		return "-"
	default:
		return "pending"
	}
}

func shortVoteType(voteType string) string {
	switch voteType {
	case "prevote":
		return "pv"
	case "precommit":
		return "pc"
	default:
		return voteType
	}
}

func voteTypeOrder(voteType string) int {
	switch voteType {
	case "prevote":
		return 0
	case "precommit":
		return 1
	default:
		return 2
	}
}

func heightStatusText(status model.HeightStatus) string {
	switch status {
	case model.HeightClosed:
		return "closed"
	case model.HeightEvicted:
		return "evicted"
	default:
		return "active"
	}
}

func timeDisplayPrecision(latency time.Duration) time.Duration {
	if latency >= time.Second {
		return 10 * time.Millisecond
	}
	return time.Millisecond
}

// sortByGenesisIndex sorts names using the canonical validator ordering:
// genesis-indexed nodes first (ascending), then unknowns alphabetically.
func sortByGenesisIndex(names []string, genesisIdxOf map[string]int) {
	sort.SliceStable(names, func(i, j int) bool {
		ii, iok := genesisIdxOf[names[i]]
		ji, jok := genesisIdxOf[names[j]]
		if !iok {
			ii = -1
		}
		if !jok {
			ji = -1
		}
		switch {
		case ii >= 0 && ji >= 0:
			return ii < ji
		case ii >= 0:
			return true
		case ji >= 0:
			return false
		default:
			return names[i] < names[j]
		}
	})
}

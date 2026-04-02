package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/gnolang/gno/tm2/pkg/colors"
)

// TextOptions controls the text renderer behaviour.
type TextOptions struct {
	Verbose          bool
	ShowUnclassified bool // show parser warnings for unclassified log lines
	MaxFindings      int  // 0 = unlimited
	MaxHealth        int  // max node sections in health summary (0 = unlimited)
	Color            bool // emit ANSI color codes (auto-detected from TTY)
}

// colorizer wraps ANSI helpers; a no-op when Color is false.
type colorizer struct{ enabled bool }

func (c colorizer) apply(code, s string) string {
	if !c.enabled || s == "" {
		return s
	}
	return code + s + colors.ANSIReset
}

func (c colorizer) bold(s string) string   { return c.apply(colors.ANSIBright, s) }
func (c colorizer) dim(s string) string    { return c.apply(colors.ANSIDim, s) }
func (c colorizer) red(s string) string    { return c.apply(colors.ANSIFgRed, s) }
func (c colorizer) yellow(s string) string { return c.apply(colors.ANSIFgYellow, s) }
func (c colorizer) green(s string) string  { return c.apply(colors.ANSIFgGreen, s) }
func (c colorizer) cyan(s string) string   { return c.apply(colors.ANSIFgCyan, s) }
func (c colorizer) gray(s string) string   { return c.apply(colors.ANSIFgGray, s) }

func (c colorizer) severity(sev model.Severity) string {
	badge := "[" + string(sev) + "]"
	if !c.enabled {
		return badge
	}
	switch sev {
	case model.SeverityCritical:
		return colors.ANSIBright + colors.ANSIFgRed + badge + colors.ANSIReset
	case model.SeverityHigh:
		return colors.ANSIBright + colors.ANSIFgYellow + badge + colors.ANSIReset
	case model.SeverityMedium:
		return colors.ANSIFgCyan + badge + colors.ANSIReset
	case model.SeverityLow:
		return colors.ANSIFgGray + badge + colors.ANSIReset
	default: // info
		return colors.ANSIFgGray + badge + colors.ANSIReset
	}
}

func Text(report model.Report, opts TextOptions) string {
	var b strings.Builder
	c := colorizer{opts.Color}

	// ── Header ──────────────────────────────────────────────────────────────
	fmt.Fprintf(&b, "Chain: %s\n", report.Input.ChainID)
	fmt.Fprintf(&b, "Genesis validators: %d\n", report.Input.ValidatorCount)
	fmt.Fprintf(&b, "Logs analyzed: %d files, %d nodes", report.Input.LogFileCount, report.Input.NodeCount)
	if report.Input.TimeWindowStart != "" || report.Input.TimeWindowEnd != "" {
		fmt.Fprintf(&b, ", window %s -> %s", emptyDash(report.Input.TimeWindowStart), emptyDash(report.Input.TimeWindowEnd))
	}
	b.WriteString("\n")

	// ── Health summary ───────────────────────────────────────────────────────
	b.WriteString("\n" + c.bold("Health summary") + "\n")
	maxCommit := int64(0)
	for _, node := range report.Nodes {
		if node.HighestCommit > maxCommit {
			maxCommit = node.HighestCommit
		}
	}
	if maxCommit > 0 {
		fmt.Fprintf(&b, "%s\n", c.green(fmt.Sprintf("- Forward progress observed until height %d", maxCommit)))
	} else {
		fmt.Fprintf(&b, "%s\n", c.red("- No finalized commit observed in the analyzed window"))
	}
	if report.MetadataGeneratedPath != "" {
		fmt.Fprintf(&b, "- Metadata template written to %s\n", report.MetadataGeneratedPath)
	}

	shown := 0
	for i, node := range report.Nodes {
		// Timeout counts are only shown in verbose mode: in default mode they
		// are either transient (node committed) or already expressed as a finding
		// ("never finalized a block"). Showing the raw count without that context
		// just creates confusion about missing findings.
		showTimeouts := node.TimeoutCount > 0 && opts.Verbose
		hasPeers := node.MaxPeers > 0
		hasSigner := node.SignerFailureCount > 0
		if !showTimeouts && !hasPeers && !hasSigner {
			continue
		}
		if opts.MaxHealth > 0 && !opts.Verbose && shown >= opts.MaxHealth {
			remaining := 0
			for _, n := range report.Nodes[i:] {
				if n.MaxPeers > 0 {
					remaining++
				}
			}
			fmt.Fprintf(&b, "%s\n", c.dim(fmt.Sprintf("- ... %d more node(s) omitted; use --verbose to see all", remaining)))
			break
		}
		shown++

		if showTimeouts {
			plural := "s"
			if node.TimeoutCount == 1 {
				plural = ""
			}
			fmt.Fprintf(&b, "- %s saw %d timeout event%s\n", node.Name, node.TimeoutCount, plural)
			if opts.Verbose {
				for _, sample := range node.TimeoutSamples {
					if sample.Path != "" {
						fmt.Fprintf(&b, "  %s:%d %s\n", sample.Path, sample.Line, sample.Message)
					} else {
						fmt.Fprintf(&b, "  %s\n", sample.Message)
					}
				}
				if node.TimeoutCount > len(node.TimeoutSamples) {
					fmt.Fprintf(&b, "  %s\n", c.dim(fmt.Sprintf("... %d more", node.TimeoutCount-len(node.TimeoutSamples))))
				}
			}
		}
		if hasPeers {
			stall := ""
			if node.StallDuration > 0 {
				stall = c.red(fmt.Sprintf(" stalled %s", formatDuration(node.StallDuration)))
			}
			fmt.Fprintf(&b, "- %s peer count max=%d current=%d%s\n", node.Name, node.MaxPeers, node.CurrentPeers, stall)
		}
		if hasSigner {
			if node.SignerConnectCount > 0 {
				fmt.Fprintf(&b, "- %s %s failures=%d reconnects=%d\n",
					node.Name, c.red("remote signer unstable:"),
					node.SignerFailureCount, node.SignerConnectCount)
			} else {
				fmt.Fprintf(&b, "- %s %s\n",
					node.Name, c.red(fmt.Sprintf("remote signer: %d failure(s), no reconnect observed", node.SignerFailureCount)))
			}
		}
	}

	// ── Consensus state ──────────────────────────────────────────────────────
	anyConsensusState := false
	for _, node := range report.Nodes {
		if node.LastHeight > 0 {
			anyConsensusState = true
			break
		}
	}
	if anyConsensusState {
		maxLastHeight := int64(0)
		for _, node := range report.Nodes {
			if node.LastHeight > maxLastHeight {
				maxLastHeight = node.LastHeight
			}
		}

		b.WriteString("\n" + c.bold("Consensus state (end of window)") + "\n")
		for _, node := range report.Nodes {
			if node.LastHeight == 0 {
				if node.Role == model.RoleValidator {
					fmt.Fprintf(&b, "- %s [%s] no consensus events observed\n", node.Name, c.dim(string(node.Role)))
				}
				continue
			}
			lag := ""
			if maxLastHeight > node.LastHeight {
				lag = c.red(fmt.Sprintf(" [!%d behind]", maxLastHeight-node.LastHeight))
			}
			step := ""
			if node.LastStep != "" {
				step = " step=" + c.dim(node.LastStep)
			}
			ts := ""
			if !node.LastEventTime.IsZero() {
				ts = c.dim(" (last: " + node.LastEventTime.UTC().Format("15:04:05Z") + ")")
			}
			fastsync := ""
			if node.JoinedViaFastSync {
				if node.FastSyncSwitchHeight > 0 {
					fastsync = c.yellow(fmt.Sprintf(" [fast-sync@h%d]", node.FastSyncSwitchHeight))
				} else {
					fastsync = c.yellow(" [fast-sync]")
				}
			}
			fmt.Fprintf(&b, "- %s [%s] height=%d round=%d%s%s%s%s\n",
				node.Name, c.dim(string(node.Role)),
				node.LastHeight, node.LastRound,
				step, ts, lag, fastsync,
			)
			if node.PrevotesTotal > 0 || node.PrecommitsTotal > 0 {
				prevMaj, precomMaj := "", ""
				if node.PrevotesMaj23 {
					prevMaj = c.green(" +2/3")
				}
				if node.PrecommitsMaj23 {
					precomMaj = c.green(" +2/3")
				}
				fmt.Fprintf(&b, "  prevotes: %d/%d%s  precommits: %d/%d%s\n",
					node.PrevotesReceived, node.PrevotesTotal, prevMaj,
					node.PrecommitsReceived, node.PrecommitsTotal, precomMaj,
				)
			}
			if node.MaxRoundSeen >= 3 {
				fmt.Fprintf(&b, "  %s max_round=%d at h%d\n",
					c.yellow("round escalation:"), node.MaxRoundSeen, node.MaxRoundHeight)
			}
			if node.ProposalSignedCount > 0 {
				fmt.Fprintf(&b, "  proposals signed: %d\n", node.ProposalSignedCount)
			}
			if node.LastAppHash != "" {
				short := node.LastAppHash
				if len(short) > 16 {
					short = short[:16] + "…"
				}
				fmt.Fprintf(&b, "  appHash h%d: %s\n", node.LastAppHashHeight, c.dim(short))
			}
			if node.StuckAtHeight > node.HighestCommit {
				fmt.Fprintf(&b, "  %s stuck trying to commit h%d (%d block(s) past last observed commit)\n",
					c.yellow("gossip:"), node.StuckAtHeight, node.StuckAtHeight-node.HighestCommit)
			}
			// Show peer gossip states when they differ from the local commit height,
			// indicating what remote peers were doing at the time of any stall.
			if len(node.PeerStates) > 0 {
				peersAhead := 0
				for _, ps := range node.PeerStates {
					if ps.Height > node.HighestCommit {
						peersAhead++
					}
				}
				if peersAhead > 0 || node.PeerVoteMaxHeight > node.HighestCommit {
					fmt.Fprintf(&b, "  %s\n", c.dim("peer gossip (last known state):"))
					for _, ps := range node.PeerStates {
						lag := ""
						if ps.Height < node.LastHeight {
							lag = c.red(fmt.Sprintf(" [%d behind]", node.LastHeight-ps.Height))
						}
						fmt.Fprintf(&b, "  %s height=%d round=%d step=%s%s\n",
							c.gray(ps.Peer), ps.Height, ps.Round, ps.Step, lag)
					}
				}
				if node.PeerVoteMaxHeight > 0 {
					voteNote := fmt.Sprintf("  last vote gossip received at h%d", node.PeerVoteMaxHeight)
					// Only flag a chain-wide halt when there is a real stall — i.e.
					// the log window extends well past the last commit. Use the same
					// 30s floor as the stall-finding threshold so a log that ends
					// a few ms after the last commit never triggers this annotation.
					if node.PeerVoteMaxHeight <= node.HighestCommit && node.StallDuration >= 30*time.Second {
						voteNote += c.red(" (zero votes for next height — chain-wide halt)")
					}
					fmt.Fprintf(&b, "%s\n", c.dim(voteNote))
				}
			}
		}
	}

	// ── Consensus stall state ────────────────────────────────────────────────
	// Per-node pipeline view (Propose → Prevote → Precommit → Commit) shown
	// whenever any node has a StallState. Discrepancies across nodes are
	// highlighted in red.
	{
		var stallNodes []model.NodeSummary
		for _, node := range report.Nodes {
			if node.StallState != nil {
				stallNodes = append(stallNodes, node)
			}
		}
		if len(stallNodes) > 0 {
			b.WriteString("\n" + c.bold("Consensus stall state") + "\n")

			// Detect cross-node discrepancies for highlighting.
			heights := map[int64]bool{}
			rounds := map[int]bool{}
			proposers := map[string]bool{}
			for _, node := range stallNodes {
				ss := node.StallState
				heights[ss.Height] = true
				rounds[ss.Round] = true
				if ss.Proposer != "" {
					proposers[ss.Proposer] = true
				}
			}
			multiHeight := len(heights) > 1
			multiRound := len(rounds) > 1
			multiProposer := len(proposers) > 1

			for _, node := range stallNodes {
				ss := node.StallState

				// Header line.
				hStr := fmt.Sprintf("h=%d", ss.Height)
				if multiHeight {
					hStr = c.red(hStr)
				}
				rStr := fmt.Sprintf("r=%d", ss.Round)
				if multiRound {
					rStr = c.red(rStr)
				}
				fmt.Fprintf(&b, "- %s %s  %s %s\n",
					node.Name, c.dim("["+ss.Source+"]"), hStr, rStr)

				// Proposer.
				if ss.Proposer != "" {
					short := ss.Proposer
					if len(short) > 20 {
						short = short[:10] + "…" + short[len(short)-8:]
					}
					label := "  proposer:  "
					val := c.dim(short)
					if multiProposer {
						val = c.red(short + "  ← mismatch")
					}
					fmt.Fprintf(&b, "%s%s\n", label, val)
				}

				rank := stallStepRank(ss.Step)

				// ── Propose step ──────────────────────────────────────────
				proposeReached := rank >= stallStepRank("Propose")
				fmt.Fprintf(&b, "  Propose:    ")
				if !proposeReached {
					fmt.Fprintf(&b, "%s\n", c.dim("not reached"))
				} else {
					parts := []string{}
					if ss.ProposalSigned {
						parts = append(parts, c.green("signed"))
					}
					if ss.ProposalReceived {
						parts = append(parts, c.green("received"))
					}
					if ss.NilPrevoteCount > 0 {
						parts = append(parts, c.red(fmt.Sprintf("no proposal (%dx nil-prevote)", ss.NilPrevoteCount)))
					}
					if ss.ProposalBlockHash != "" {
						short := ss.ProposalBlockHash
						if len(short) > 16 {
							short = short[:16] + "…"
						}
						parts = append(parts, c.dim("block="+short))
					}
					if len(parts) == 0 {
						parts = append(parts, c.dim("reached"))
					}
					fmt.Fprintf(&b, "%s\n", strings.Join(parts, "  "))
				}

				// ── Prevote step ──────────────────────────────────────────
				prevoteReached := rank >= stallStepRank("Prevote")
				fmt.Fprintf(&b, "  Prevote:    ")
				if !prevoteReached {
					fmt.Fprintf(&b, "%s\n", c.dim("not reached"))
				} else if ss.PrevotesTotal == 0 {
					suffix := ""
					if ss.Step == "Prevote" || ss.Step == "PrevoteWait" {
						suffix = c.yellow("  ← stuck here")
					}
					fmt.Fprintf(&b, "%s%s\n", c.dim("reached (no vote data)"), suffix)
				} else {
					maj := ""
					if ss.PrevotesMaj23 {
						maj = c.green(" +2/3")
					}
					suffix := ""
					if ss.Step == "Prevote" || ss.Step == "PrevoteWait" {
						suffix = c.yellow("  ← stuck here")
					}
					fmt.Fprintf(&b, "%d/%d%s%s\n", ss.PrevotesReceived, ss.PrevotesTotal, maj, suffix)
				}

				// ── Precommit step ────────────────────────────────────────
				precommitReached := rank >= stallStepRank("Precommit")
				fmt.Fprintf(&b, "  Precommit:  ")
				if !precommitReached {
					fmt.Fprintf(&b, "%s\n", c.dim("not reached"))
				} else if ss.PrecommitsTotal == 0 {
					suffix := ""
					if ss.Step == "Precommit" || ss.Step == "PrecommitWait" {
						suffix = c.yellow("  ← stuck here")
					}
					fmt.Fprintf(&b, "%s%s\n", c.dim("reached (no vote data)"), suffix)
				} else {
					maj := ""
					if ss.PrecommitsMaj23 {
						maj = c.green(" +2/3")
					}
					suffix := ""
					if ss.Step == "Precommit" || ss.Step == "PrecommitWait" {
						suffix = c.yellow("  ← stuck here")
					}
					fmt.Fprintf(&b, "%d/%d%s%s\n", ss.PrecommitsReceived, ss.PrecommitsTotal, maj, suffix)
				}

				// ── Commit ────────────────────────────────────────────────
				fmt.Fprintf(&b, "  Commit:     %s\n", c.red("not committed"))

				if ss.LockedBlockHash != "" {
					short := ss.LockedBlockHash
					if len(short) > 16 {
						short = short[:16] + "…"
					}
					fmt.Fprintf(&b, "  locked:     %s\n", c.dim(short))
				}
			}
		}
	}

	// ── Findings ─────────────────────────────────────────────────────────────
	b.WriteString("\n" + c.bold("Findings") + "\n")
	rendered := 0
	for _, finding := range report.Findings {
		if !opts.Verbose && (finding.Severity == model.SeverityInfo || finding.Severity == model.SeverityLow) {
			continue
		}
		rendered++
		if opts.MaxFindings > 0 && rendered > opts.MaxFindings {
			break
		}
		if rendered > 1 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s\n", c.severity(finding.Severity), c.bold(finding.Title))
		fmt.Fprintf(&b, "  %s\n", finding.Summary)
		for _, evidence := range finding.Evidence {
			if evidence.Message == "" {
				continue
			}
			prefix := c.gray("evidence:")
			if evidence.Path != "" {
				fmt.Fprintf(&b, "  %s %s:%d %s\n", prefix, evidence.Path, evidence.Line, evidence.Message)
			} else if evidence.Node != "" {
				fmt.Fprintf(&b, "  %s [%s] %s\n", prefix, evidence.Node, evidence.Message)
			} else {
				fmt.Fprintf(&b, "  %s %s\n", prefix, evidence.Message)
			}
		}
		for _, cause := range finding.PossibleCauses {
			fmt.Fprintf(&b, "  %s %s\n", c.yellow("possible cause:"), cause)
		}
		for _, action := range finding.SuggestedActions {
			fmt.Fprintf(&b, "  %s %s\n", c.cyan("suggested:"), action)
		}
	}

	// ── Unclassified log lines ────────────────────────────────────────────────
	if opts.ShowUnclassified && len(report.UnclassifiedCounts) > 0 {
		total := 0
		for _, e := range report.UnclassifiedCounts {
			total += e.Count
		}
		fmt.Fprintf(&b, "\n%s (%s total)\n", c.bold("Unclassified log lines"), formatCount(total))
		maxCount := report.UnclassifiedCounts[0].Count
		countWidth := len(fmt.Sprintf("%d", maxCount))
		idxWidth := len(fmt.Sprintf("%d", len(report.UnclassifiedCounts)))
		for i, e := range report.UnclassifiedCounts {
			fmt.Fprintf(&b, "  %*d. %*d  %-60s (first: %s:%d)\n",
				idxWidth, i+1,
				countWidth, e.Count,
				truncate(e.Message, 60),
				e.FirstPath, e.FirstLine,
			)
		}
		fmt.Fprintf(&b, "\n  %s\n", c.dim("tip: use -category N to browse all lines in a category"))
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func stallStepRank(step string) int {
	switch step {
	case "NewHeight":
		return 1
	case "NewRound":
		return 2
	case "Propose":
		return 3
	case "Prevote":
		return 4
	case "PrevoteWait":
		return 5
	case "Precommit":
		return 6
	case "PrecommitWait":
		return 7
	case "Commit":
		return 8
	}
	return 0
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func formatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}
	return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n/1000)%1000, n%1000)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

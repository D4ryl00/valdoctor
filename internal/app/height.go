package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/D4ryl00/valdoctor/internal/analyze"
	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/parse"
	"github.com/D4ryl00/valdoctor/internal/render"
	"github.com/D4ryl00/valdoctor/internal/rpc"
	"github.com/gnolang/gno/tm2/pkg/commands"
)

type heightCfg struct {
	configPath    string
	genesisPath   string
	logPaths      multiString
	validatorLogs multiString
	sentryLogs    multiString
	metadataPaths multiString
	nodeBindings  multiString
	roleBindings  multiString
	node          string // --node for single-node view
	offline       bool
}

func newHeightCmd(io commands.IO) *commands.Command {
	cfg := &heightCfg{}

	return commands.NewCommand(
		commands.Metadata{
			Name:       "height",
			ShortUsage: "height <N> [flags]",
			ShortHelp:  "analyse consensus, votes, clock sync, and peer state at a specific block height",
		},
		cfg,
		func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return errors.New("expected exactly one argument: block height")
			}
			height, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || height <= 0 {
				return fmt.Errorf("invalid height %q: must be a positive integer", args[0])
			}
			return execHeight(ctx, cfg, height, io)
		},
	)
}

func (c *heightCfg) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "path to a TOML config file (default: $XDG_CONFIG_HOME/valdoctor/config.toml)")
	fs.StringVar(&c.genesisPath, "genesis", "", "path to genesis.json")
	fs.Var(&c.logPaths, "log", "generic log file path; may be repeated")
	fs.Var(&c.validatorLogs, "validator-log", "validator log file path; may be repeated")
	fs.Var(&c.sentryLogs, "sentry-log", "sentry log file path; may be repeated")
	fs.Var(&c.metadataPaths, "metadata", "TOML metadata file path; may be repeated")
	fs.Var(&c.nodeBindings, "node-binding", "bind a node name to a log path as <name>=<path>")
	fs.Var(&c.roleBindings, "role", "assign a role to a node as <name>=<role>")
	fs.StringVar(&c.node, "node", "", "restrict vote grid and peer table to a single node (default: aggregate)")
	fs.BoolVar(&c.offline, "offline", false, "disable all network calls (skip RPC enrichment)")
}

func execHeight(_ context.Context, cfg *heightCfg, height int64, io commands.IO) error {
	ucfg, err := loadConfig(cfg.configPath)
	if err != nil {
		return err
	}

	if cfg.genesisPath == "" {
		return errors.New("missing required --genesis")
	}

	genesisPath := parse.NormalizePath(cfg.genesisPath)
	genesis, err := parse.LoadGenesis(genesisPath)
	if err != nil {
		return err
	}

	metadata, _, err := loadAndMergeMetadata(cfg.metadataPaths)
	if err != nil {
		return err
	}

	// Build sources, reusing the same logic as inspect.
	// heightCfg mirrors the relevant fields of inspectCfg.
	inspCfg := &inspectCfg{
		logPaths:      cfg.logPaths,
		validatorLogs: cfg.validatorLogs,
		sentryLogs:    cfg.sentryLogs,
		nodeBindings:  cfg.nodeBindings,
		roleBindings:  cfg.roleBindings,
	}
	// Apply metadata node bindings.
	for name, node := range metadata.Nodes {
		for _, file := range node.Files {
			inspCfg.nodeBindings = append(inspCfg.nodeBindings, name+"="+file)
		}
	}
	_ = ucfg // loaded for future config options

	sources, err := buildSources(inspCfg, metadata)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return errors.New("at least one log file is required")
	}

	// Validate --node flag against known sources.
	if cfg.node != "" {
		found := false
		for _, s := range sources {
			if s.Node == cfg.node {
				found = true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(sources))
			for _, s := range sources {
				names = append(names, s.Node)
			}
			return fmt.Errorf("--node %q not found; known nodes: %s", cfg.node, joinNames(names))
		}
	}

	// Parse log files: only collect events relevant to this height.
	var allEvents []model.Event
	var warnings []string

	for _, source := range sources {
		rc, openErr := openLogFile(source.Path)
		if openErr != nil {
			return fmt.Errorf("unable to open log %s: %w", source.Path, openErr)
		}
		fileEvents, parseErr := parse.FilterEventsByHeight(source, rc, height)
		rc.Close()
		if parseErr != nil {
			return fmt.Errorf("error reading log %s: %w", source.Path, parseErr)
		}
		allEvents = append(allEvents, fileEvents...)
	}

	sort.SliceStable(allEvents, func(i, j int) bool {
		if allEvents[i].HasTimestamp && allEvents[j].HasTimestamp {
			if allEvents[i].Timestamp.Equal(allEvents[j].Timestamp) {
				return allEvents[i].Line < allEvents[j].Line
			}
			return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
		}
		if allEvents[i].HasTimestamp != allEvents[j].HasTimestamp {
			return allEvents[i].HasTimestamp
		}
		return allEvents[i].Line < allEvents[j].Line
	})

	// ── RPC enrichment ────────────────────────────────────────────────────────
	var blockHeader *rpc.BlockHeader
	var commitSigs []rpc.CommitSig
	var txResults []model.TxSummary

	if !cfg.offline {
		// Use the first node with an RPC endpoint.
		for _, nodeName := range rpcNodeOrder(metadata) {
			node := metadata.Nodes[nodeName]
			if node.RPCEndpoint == "" {
				continue
			}
			ep := node.RPCEndpoint

			if bh, fetchErr := rpc.FetchBlock(ep, height); fetchErr == nil {
				blockHeader = &bh
			} else {
				warnings = append(warnings, fmt.Sprintf("RPC /block@%s: %v", ep, fetchErr))
			}

			if sigs, fetchErr := rpc.FetchCommit(ep, height); fetchErr == nil {
				commitSigs = sigs
			} else {
				warnings = append(warnings, fmt.Sprintf("RPC /commit@%s: %v", ep, fetchErr))
			}

			if br, fetchErr := rpc.FetchBlockResults(ep, height); fetchErr == nil {
				txResults = make([]model.TxSummary, len(br.DeliverTxs))
				for i, tx := range br.DeliverTxs {
					txResults[i] = model.TxSummary{
						GasWanted: tx.GasWanted,
						GasUsed:   tx.GasUsed,
						Error:     tx.Error,
					}
				}
			} else {
				warnings = append(warnings, fmt.Sprintf("RPC /block_results@%s: %v", ep, fetchErr))
			}
			break // only query one RPC endpoint for now
		}
	}

	// ── Build report ─────────────────────────────────────────────────────────
	report := analyze.BuildHeightReport(analyze.HeightInput{
		Height:     height,
		Genesis:    genesis,
		Sources:    sources,
		Metadata:   metadata,
		Events:     allEvents,
		Block:      blockHeader,
		CommitSigs: commitSigs,
		TxResults:  txResults,
		FocusNode:  cfg.node,
	})
	report.Warnings = append(report.Warnings, warnings...)

	// ── Render ────────────────────────────────────────────────────────────────
	colorOutput := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
	io.Printf("%s", render.HeightText(report, colorOutput))
	return nil
}

// rpcNodeOrder returns metadata node names that have an RPC endpoint, sorted
// alphabetically for determinism.
func rpcNodeOrder(meta model.Metadata) []string {
	names := make([]string, 0, len(meta.Nodes))
	for name, node := range meta.Nodes {
		if node.RPCEndpoint != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func joinNames(names []string) string {
	sort.Strings(names)
	return "[" + strings.Join(names, ", ") + "]"
}

package app

import (
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/D4ryl00/valdoctor/internal/analyze"
	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/parse"
	"github.com/D4ryl00/valdoctor/internal/render"
	"github.com/D4ryl00/valdoctor/internal/rpc"
	"github.com/gnolang/gno/tm2/pkg/commands"
)

type inspectCfg struct {
	configPath       string
	genesisPath      string
	logPaths         multiString
	validatorLogs    multiString
	sentryLogs       multiString
	metadataPaths    multiString
	generateMetadata string
	nodeBindings     multiString
	roleBindings     multiString
	outputFormat     string
	sinceRaw         string
	untilRaw         string
	verbose          bool
	showUnclassified bool
	maxFindings      int
	maxHealth        int
	categoryN        int
	offline          bool
}

type multiString []string

func (m *multiString) String() string { return strings.Join(*m, ",") }
func (m *multiString) Set(value string) error {
	// Expand glob patterns so callers can write --log 'logs/val*.log'.
	// If the pattern contains no glob metacharacters, or it matches nothing,
	// keep the original value (filepath.Glob returns nil on no match).
	if strings.ContainsAny(value, "*?[") {
		matches, err := filepath.Glob(value)
		if err != nil {
			return fmt.Errorf("invalid glob pattern %q: %w", value, err)
		}
		if len(matches) > 0 {
			*m = append(*m, matches...)
			return nil
		}
	}
	*m = append(*m, value)
	return nil
}

func newInspectCmd(io commands.IO) *commands.Command {
	cfg := &inspectCfg{}

	return commands.NewCommand(
		commands.Metadata{
			Name:       "inspect",
			ShortUsage: "inspect [flags]",
			ShortHelp:  "inspect genesis and logs and produce a diagnosis report",
		},
		cfg,
		func(ctx context.Context, args []string) error {
			// Positional arguments are treated as additional --log paths so
			// shell globs work: `val*.log` expands into separate argv entries.
			for _, arg := range args {
				if err := cfg.logPaths.Set(arg); err != nil {
					return fmt.Errorf("invalid log path %q: %w", arg, err)
				}
			}
			return execInspect(ctx, cfg, io)
		},
	)
}

func (c *inspectCfg) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "path to a TOML config file (default: $XDG_CONFIG_HOME/valdoctor/config.toml)")
	fs.StringVar(&c.genesisPath, "genesis", "", "path to the genesis.json")
	fs.Var(&c.logPaths, "log", "generic log file path; may be repeated")
	fs.Var(&c.validatorLogs, "validator-log", "validator log file path; may be repeated")
	fs.Var(&c.sentryLogs, "sentry-log", "sentry log file path; may be repeated")
	fs.Var(&c.metadataPaths, "metadata", "TOML metadata file path; may be repeated")
	fs.StringVar(&c.generateMetadata, "generate-metadata", "", "write inferred TOML metadata during inspection")
	fs.Var(&c.nodeBindings, "node", "bind a node name to a log path as <name>=<path>")
	fs.Var(&c.roleBindings, "role", "assign a role to a node as <name>=<role>")
	fs.StringVar(&c.outputFormat, "format", "", "report format: text or json (default: text)")
	fs.StringVar(&c.sinceRaw, "since", "", "lower bound of the analysis window in RFC3339")
	fs.StringVar(&c.untilRaw, "until", "", "upper bound of the analysis window in RFC3339")
	fs.BoolVar(&c.verbose, "verbose", false, "include low-severity findings; show event details in health summary")
	fs.BoolVar(&c.verbose, "v", false, "shorthand for -verbose")
	fs.BoolVar(&c.showUnclassified, "show-unclassified", false, "print unclassified log lines at the end of the report")
	fs.IntVar(&c.maxFindings, "max-findings", 0, "maximum number of findings rendered in text output (default: 20)")
	fs.IntVar(&c.maxHealth, "max-health", 0, "maximum number of nodes shown in health summary (default: 5; 0 in verbose)")
	fs.IntVar(&c.categoryN, "category", 0, "browse all log lines for unclassified category N (from -show-unclassified table)")
	fs.BoolVar(&c.offline, "offline", false, "disable all network calls (skip RPC enrichment even when endpoints are configured)")
}

func execInspect(_ context.Context, cfg *inspectCfg, io commands.IO) error {
	// Load config file and apply defaults for flags left at their sentinel (zero) values.
	ucfg, err := loadConfig(cfg.configPath)
	if err != nil {
		return err
	}
	cfg.verbose = cfg.verbose || ucfg.Verbose
	if cfg.outputFormat == "" {
		if ucfg.Format != "" {
			cfg.outputFormat = ucfg.Format
		} else {
			cfg.outputFormat = "text"
		}
	}
	if cfg.maxFindings == 0 {
		if ucfg.MaxFindings > 0 {
			cfg.maxFindings = ucfg.MaxFindings
		} else {
			cfg.maxFindings = 20
		}
	}
	if cfg.maxHealth == 0 && !cfg.verbose {
		if ucfg.MaxHealth > 0 {
			cfg.maxHealth = ucfg.MaxHealth
		} else {
			cfg.maxHealth = 5
		}
	}

	if cfg.genesisPath == "" {
		return errors.New("missing required --genesis")
	}

	since, until, err := parseWindow(cfg.sinceRaw, cfg.untilRaw)
	if err != nil {
		return err
	}

	genesisPath := parse.NormalizePath(cfg.genesisPath)
	genesis, err := parse.LoadGenesis(genesisPath)
	if err != nil {
		return err
	}
	genesis.Path = genesisPath

	metadata, metadataWarnings, err := loadAndMergeMetadata(cfg.metadataPaths)
	if err != nil {
		return err
	}

	sources, err := buildSources(cfg, metadata)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return errors.New("at least one log file is required")
	}

	events := make([]model.Event, 0)
	warnings := append([]string(nil), metadataWarnings...)
	unclassified := map[string]model.UnclassifiedEntry{}
	peerStatsByNode := map[string]parse.ParseStats{}

	for _, source := range sources {
		rc, openErr := openLogFile(source.Path)
		if openErr != nil {
			return fmt.Errorf("unable to open log %s: %w", source.Path, openErr)
		}
		fileEvents, fileWarnings, fileUnclassified, fileStats, parseErr := parse.ParseLogFile(source, rc)
		rc.Close()
		if parseErr != nil {
			return fmt.Errorf("error reading log %s: %w", source.Path, parseErr)
		}
		events = append(events, filterWindow(fileEvents, since, until)...)
		warnings = append(warnings, fileWarnings...)
		for msg, entry := range fileUnclassified {
			agg := unclassified[msg]
			agg.Count += entry.Count
			if agg.Count == entry.Count {
				agg.Message = entry.Message
				agg.FirstPath = entry.FirstPath
				agg.FirstLine = entry.FirstLine
			}
			unclassified[msg] = agg
		}
		// Merge peer stats per node (last-wins for individual peer states since
		// they are already kept at max height during parsing).
		existing := peerStatsByNode[source.Node]
		if fileStats.MaxPeerVoteHeight > existing.MaxPeerVoteHeight {
			existing.MaxPeerVoteHeight = fileStats.MaxPeerVoteHeight
		}
		if fileStats.StuckHeight > existing.StuckHeight {
			existing.StuckHeight = fileStats.StuckHeight
		}
		if existing.PeerRoundStates == nil {
			existing.PeerRoundStates = map[string]model.PeerRoundState{}
		}
		for addr, rs := range fileStats.PeerRoundStates {
			prev, ok := existing.PeerRoundStates[addr]
			if !ok || rs.Height > prev.Height || (rs.Height == prev.Height && rs.Round > prev.Round) {
				existing.PeerRoundStates[addr] = rs
			}
		}
		peerStatsByNode[source.Node] = existing
	}

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].HasTimestamp && events[j].HasTimestamp {
			if events[i].Timestamp.Equal(events[j].Timestamp) {
				if events[i].Path == events[j].Path {
					return events[i].Line < events[j].Line
				}
				return events[i].Path < events[j].Path
			}
			return events[i].Timestamp.Before(events[j].Timestamp)
		}
		if events[i].HasTimestamp != events[j].HasTimestamp {
			return events[i].HasTimestamp
		}
		if events[i].Path == events[j].Path {
			return events[i].Line < events[j].Line
		}
		return events[i].Path < events[j].Path
	})

	// Convert parse.ParseStats → analyze.NodePeerStats for each node.
	analyzePeerStats := map[string]analyze.NodePeerStats{}
	for node, ps := range peerStatsByNode {
		analyzePeerStats[node] = analyze.NodePeerStats{
			MaxVoteHeight: ps.MaxPeerVoteHeight,
			RoundStates:   ps.PeerRoundStates,
			StuckHeight:   ps.StuckHeight,
		}
	}

	report := analyze.BuildReport(analyze.Input{
		Genesis:            genesis,
		Sources:            sources,
		Events:             events,
		Warnings:           warnings,
		UnclassifiedCounts: unclassified,
		Verbose:            cfg.verbose,
		Metadata:           metadata,
		PeerStatsByNode:    analyzePeerStats,
	})

	// ── RPC enrichment ──────────────────────────────────────────────────────
	// When metadata provides RPC endpoints and --offline is not set, call
	// /block_results on the height where AppHash divergence was detected to
	// add tx-level evidence to the finding.
	if !cfg.offline {
		enrichWithRPC(&report, metadata)
	}

	var generationErr error
	if cfg.generateMetadata != "" {
		outPath := parse.NormalizePath(cfg.generateMetadata)
		if _, statErr := os.Stat(outPath); statErr == nil {
			generationErr = fmt.Errorf("metadata output path already exists: %s", outPath)
		} else {
			meta := parse.BuildGeneratedMetadata(genesis, sources)
			if writeErr := parse.WriteMetadata(outPath, meta); writeErr != nil {
				generationErr = fmt.Errorf("unable to write generated metadata %s: %w", outPath, writeErr)
			} else {
				report.MetadataGeneratedPath = outPath
			}
		}
	}
	report.Warnings = append([]string(nil), warnings...)

	// ── Category browser ────────────────────────────────────────────────────
	if cfg.categoryN > 0 {
		counts := report.UnclassifiedCounts
		if cfg.categoryN > len(counts) {
			return fmt.Errorf("category %d out of range (1-%d)", cfg.categoryN, len(counts))
		}
		targetKey := counts[cfg.categoryN-1].Message

		w, closeW := openPager()
		defer closeW()

		for _, source := range sources {
			rc, openErr := openLogFile(source.Path)
			if openErr != nil {
				return fmt.Errorf("unable to open log %s: %w", source.Path, openErr)
			}
			streamErr := parse.StreamCategoryLines(source, rc, targetKey, w)
			rc.Close()
			if streamErr != nil {
				return fmt.Errorf("error reading log %s: %w", source.Path, streamErr)
			}
		}
		return nil
	}

	// Enable colors only when stdout is a real terminal and NO_COLOR is unset.
	colorOutput := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""

	switch cfg.outputFormat {
	case "text":
		io.Printf("%s", render.Text(report, render.TextOptions{
			Verbose:          cfg.verbose,
			ShowUnclassified: cfg.showUnclassified,
			MaxFindings:      cfg.maxFindings,
			MaxHealth:        cfg.maxHealth,
			Color:            colorOutput,
		}))
	case "json":
		payload, jsonErr := render.JSON(report)
		if jsonErr != nil {
			return jsonErr
		}
		io.Printf("%s\n", payload)
	default:
		return fmt.Errorf("unsupported --format %q", cfg.outputFormat)
	}

	if generationErr != nil {
		io.ErrPrintln(generationErr.Error())
		return commands.ExitCodeError(2)
	}

	if report.CriticalIssuesDetected {
		return commands.ExitCodeError(1)
	}
	if report.ConfidenceTooLow {
		return commands.ExitCodeError(3)
	}
	return nil
}

func loadAndMergeMetadata(paths []string) (model.Metadata, []string, error) {
	items := make([]model.Metadata, 0, len(paths))
	warnings := make([]string, 0)
	for _, path := range paths {
		normalized := parse.NormalizePath(path)
		item, err := parse.LoadMetadata(normalized)
		if err != nil {
			return model.Metadata{}, nil, fmt.Errorf("unable to load metadata %s: %w", normalized, err)
		}
		for name, node := range item.Nodes {
			for index, file := range node.Files {
				itemNode := item.Nodes[name]
				itemNode.Files[index] = parse.NormalizePath(file)
				item.Nodes[name] = itemNode
			}
		}
		items = append(items, item)
	}
	merged := parse.MergeMetadata(items...)
	if merged.Version == 0 {
		warnings = append(warnings, "metadata version not set; defaulting to version 1")
	}
	return merged, warnings, nil
}

func buildSources(cfg *inspectCfg, meta model.Metadata) ([]model.Source, error) {
	return buildSourcesFromParams(cfg.logPaths, cfg.validatorLogs, cfg.sentryLogs, cfg.nodeBindings, cfg.roleBindings, meta)
}

func buildSourcesFromParams(logPaths, validatorLogs, sentryLogs, nodeBindings, roleBindings []string, meta model.Metadata) ([]model.Source, error) {
	nodeByPath := map[string]string{}
	roleByNode := map[string]model.Role{}
	usedNames := map[string]int{}

	for name, node := range meta.Nodes {
		roleByNode[name] = model.ParseRole(node.Role)
		for _, file := range node.Files {
			nodeByPath[file] = name
		}
	}

	for _, binding := range nodeBindings {
		name, path, err := splitBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("invalid --node value %q: %w", binding, err)
		}
		nodeByPath[parse.NormalizePath(path)] = name
	}

	for _, binding := range roleBindings {
		name, rawRole, err := splitBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("invalid --role value %q: %w", binding, err)
		}
		role := model.ParseRole(rawRole)
		if role == model.RoleUnknown && rawRole != string(model.RoleUnknown) {
			return nil, fmt.Errorf("invalid role %q", rawRole)
		}
		roleByNode[name] = role
	}

	type pendingSource struct {
		path string
		role model.Role
	}
	pending := make([]pendingSource, 0, len(logPaths)+len(validatorLogs)+len(sentryLogs))
	for _, path := range logPaths {
		pending = append(pending, pendingSource{path: parse.NormalizePath(path), role: model.RoleUnknown})
	}
	for _, path := range validatorLogs {
		pending = append(pending, pendingSource{path: parse.NormalizePath(path), role: model.RoleValidator})
	}
	for _, path := range sentryLogs {
		pending = append(pending, pendingSource{path: parse.NormalizePath(path), role: model.RoleSentry})
	}

	seen := map[string]model.Source{}
	for _, item := range pending {
		source, ok := seen[item.path]
		if !ok {
			nodeName := nodeByPath[item.path]
			explicitNode := nodeName != ""
			if nodeName == "" {
				nodeName = parse.DefaultNodeName(item.path, usedNames)
			}
			role := item.role
			if role == model.RoleUnknown {
				if mapped, ok := roleByNode[nodeName]; ok {
					role = mapped
				}
			}
			if role == model.RoleUnknown {
				if metaNode, ok := meta.Nodes[nodeName]; ok {
					role = model.ParseRole(metaNode.Role)
				}
			}
			source = model.Source{
				Path:         item.path,
				Node:         nodeName,
				Role:         role,
				ExplicitNode: explicitNode,
				ExplicitRole: item.role != model.RoleUnknown,
			}
		} else if source.Role == model.RoleUnknown && item.role != model.RoleUnknown {
			source.Role = item.role
			source.ExplicitRole = true
		}
		if source.Role == model.RoleUnknown {
			if mapped, ok := roleByNode[source.Node]; ok {
				source.Role = mapped
			}
		}
		seen[item.path] = source
	}

	for name := range roleByNode {
		if _, ok := meta.Nodes[name]; !ok {
			found := false
			for _, source := range seen {
				if source.Node == name {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("--role references unknown node %q", name)
			}
		}
	}

	sources := make([]model.Source, 0, len(seen))
	for _, source := range seen {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Path < sources[j].Path
	})

	return sources, nil
}

func splitBinding(raw string) (string, string, error) {
	parts := strings.SplitN(raw, "=", 2)
	if len(parts) != 2 {
		return "", "", errors.New("expected <key>=<value>")
	}
	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])
	if left == "" || right == "" {
		return "", "", errors.New("empty key or value")
	}
	return left, right, nil
}

func parseWindow(sinceRaw, untilRaw string) (time.Time, time.Time, error) {
	var since time.Time
	var until time.Time
	var err error

	if sinceRaw != "" {
		since, err = time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --since value: %w", err)
		}
	}
	if untilRaw != "" {
		until, err = time.Parse(time.RFC3339, untilRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --until value: %w", err)
		}
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return time.Time{}, time.Time{}, errors.New("--until must be greater than or equal to --since")
	}

	return since.UTC(), until.UTC(), nil
}

func openLogFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &gzipReadCloser{Reader: gr, f: f}, nil
	}
	return f, nil
}

type gzipReadCloser struct {
	*gzip.Reader
	f *os.File
}

func (g *gzipReadCloser) Close() error {
	g.Reader.Close()
	return g.f.Close()
}

// openPager returns a writer and a closer. When stdout is a terminal it
// spawns $PAGER (default: less) and pipes output through it; otherwise it
// writes directly to stdout.
func openPager() (io.Writer, func()) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return os.Stdout, func() {}
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less"
	}
	if _, err := exec.LookPath(pager); err != nil {
		return os.Stdout, func() {}
	}
	pr, pw := io.Pipe()
	cmd := exec.Command(pager)
	cmd.Stdin = pr
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return os.Stdout, func() {}
	}
	return pw, func() {
		pw.Close()
		cmd.Wait()
	}
}

// enrichWithRPC looks for AppHash-divergence findings in the report and, for
// each one, calls /block_results on every node that has an RPC endpoint
// configured in the metadata. It appends the tx-level evidence as additional
// evidence items on the finding so the operator can immediately see which
// transaction produced different gas/state across validators.
func enrichWithRPC(report *model.Report, meta model.Metadata) {
	// Collect all nodes that have an RPC endpoint configured.
	type rpcNode struct {
		name     string
		endpoint string
		hasLogs  bool // true if a NodeSummary was built from logs for this node
	}
	loggedNodes := map[string]bool{}
	for _, ns := range report.Nodes {
		loggedNodes[ns.Name] = true
	}
	var rpcNodes []rpcNode
	for name, node := range meta.Nodes {
		if node.RPCEndpoint == "" {
			continue
		}
		rpcNodes = append(rpcNodes, rpcNode{
			name:     name,
			endpoint: node.RPCEndpoint,
			hasLogs:  loggedNodes[name],
		})
	}
	if len(rpcNodes) == 0 {
		return
	}
	sort.Slice(rpcNodes, func(i, j int) bool { return rpcNodes[i].name < rpcNodes[j].name })

	// Phase 1: probe every RPC node for its live status (height + AppHash) and
	// current consensus round state.
	type nodeStatus struct {
		rpcNode
		info    rpc.ABCIInfo
		csState rpc.ConsensusState
		csErr   error
		infoErr error
	}
	statuses := make([]nodeStatus, len(rpcNodes))
	for i, n := range rpcNodes {
		info, infoErr := rpc.FetchABCIInfo(n.endpoint)
		csState, csErr := rpc.FetchConsensusState(n.endpoint)
		statuses[i] = nodeStatus{n, info, csState, csErr, infoErr}
	}

	// Build a name→index map for fast lookup into report.Nodes.
	nodeIndex := map[string]int{}
	for i, ns := range report.Nodes {
		nodeIndex[ns.Name] = i
	}

	// Populate StallState from RPC for all nodes that have a consensus state.
	for _, s := range statuses {
		if s.csErr != nil {
			continue
		}
		stallFromRPC := &model.StallConsensusState{
			Source:             "rpc",
			Height:             s.csState.Height,
			Round:              s.csState.Round,
			Step:               s.csState.Step,
			ProposalBlockHash:  s.csState.ProposalBlockHash,
			LockedBlockHash:    s.csState.LockedBlockHash,
			PrevotesReceived:   s.csState.PrevotesReceived,
			PrevotesTotal:      s.csState.PrevotesTotal,
			PrevotesMaj23:      s.csState.PrevotesMaj23,
			PrevotesBitArray:   s.csState.PrevotesBitArray,
			PrecommitsReceived: s.csState.PrecommitsReceived,
			PrecommitsTotal:    s.csState.PrecommitsTotal,
			PrecommitsMaj23:    s.csState.PrecommitsMaj23,
			PrecommitsBitArray: s.csState.PrecommitsBitArray,
		}
		if idx, ok := nodeIndex[s.name]; ok {
			updateNodeConsensusFromRPC(&report.Nodes[idx], s.csState)

			// Node has logs: only adopt RPC stall state if logs had none,
			// or if RPC shows a more advanced height.
			existing := report.Nodes[idx].StallState
			if existing == nil || stallFromRPC.Height > existing.Height {
				report.Nodes[idx].StallState = stallFromRPC
			} else if stallFromRPC.Height == existing.Height {
				// Merge: fill in proposal hashes that logs don't carry.
				if existing.ProposalBlockHash == "" {
					existing.ProposalBlockHash = stallFromRPC.ProposalBlockHash
				}
				if existing.LockedBlockHash == "" {
					existing.LockedBlockHash = stallFromRPC.LockedBlockHash
				}
			}
		} else {
			// RPC-only node: create a minimal NodeSummary so it appears in the
			// consensus and stall comparison tables.
			role := model.RoleValidator
			if mn, ok := meta.Nodes[s.name]; ok {
				role = model.ParseRole(mn.Role)
			}
			summary := model.NodeSummary{
				Name:       s.name,
				Role:       role,
				StallState: stallFromRPC,
			}
			updateNodeConsensusFromRPC(&summary, s.csState)
			report.Nodes = append(report.Nodes, summary)
		}
	}

	sort.Slice(report.Nodes, func(i, j int) bool {
		return report.Nodes[i].Name < report.Nodes[j].Name
	})

	// Phase 2: report live status for RPC-only nodes (no logs were provided for them).
	var rpcOnlyEvidence []model.Evidence
	for _, s := range statuses {
		if s.hasLogs {
			continue
		}
		if s.infoErr != nil {
			rpcOnlyEvidence = append(rpcOnlyEvidence, model.Evidence{
				Node:    s.name,
				Message: fmt.Sprintf("RPC unreachable: %v", s.infoErr),
			})
		} else {
			rpcOnlyEvidence = append(rpcOnlyEvidence, model.Evidence{
				Node:    s.name,
				Message: fmt.Sprintf("height=%d app_hash=%s", s.info.LastBlockHeight, s.info.LastBlockAppHash),
			})
		}
	}
	if len(rpcOnlyEvidence) > 0 {
		report.Findings = append(report.Findings, model.Finding{
			ID:         "rpc-node-live-status",
			Title:      "Live status of validators without logs",
			Severity:   model.SeverityInfo,
			Confidence: model.ConfidenceHigh,
			Scope:      "global",
			Summary:    "These validators were not included in the analyzed logs. Their current height and AppHash were fetched via RPC.",
			Evidence:   rpcOnlyEvidence,
			SuggestedActions: []string{
				"collect logs from these nodes and re-run to get a complete picture",
				"cross-reference their AppHash with the apphash-divergence finding to see whether they are on the correct branch",
			},
		})
	}

	// Phase 3: enrich the apphash-divergence finding with live ABCI info and
	// block_results at the divergence height from all reachable RPC nodes.
	for i, finding := range report.Findings {
		if finding.ID != "apphash-divergence" {
			continue
		}
		var divergenceHeight int64
		fmt.Sscanf(finding.Title, "AppHash divergence between validators at height %d", &divergenceHeight)

		for _, s := range statuses {
			if s.infoErr != nil {
				report.Findings[i].Evidence = append(report.Findings[i].Evidence, model.Evidence{
					Node:    s.name,
					Message: fmt.Sprintf("RPC unreachable: %v", s.infoErr),
				})
				continue
			}
			report.Findings[i].Evidence = append(report.Findings[i].Evidence, model.Evidence{
				Node:    s.name,
				Message: fmt.Sprintf("live state: height=%d app_hash=%s", s.info.LastBlockHeight, s.info.LastBlockAppHash),
			})

			if divergenceHeight <= 0 {
				continue
			}
			br, err := rpc.FetchBlockResults(s.endpoint, divergenceHeight)
			if err != nil {
				report.Findings[i].Evidence = append(report.Findings[i].Evidence, model.Evidence{
					Node:    s.name,
					Message: fmt.Sprintf("block_results h%d: fetch failed: %v", divergenceHeight, err),
				})
				continue
			}
			if len(br.DeliverTxs) == 0 {
				report.Findings[i].Evidence = append(report.Findings[i].Evidence, model.Evidence{
					Node:    s.name,
					Message: fmt.Sprintf("block_results h%d: no transactions", divergenceHeight),
				})
				continue
			}
			for idx, tx := range br.DeliverTxs {
				msg := fmt.Sprintf("block_results h%d tx[%d]: gas_wanted=%d gas_used=%d",
					divergenceHeight, idx, tx.GasWanted, tx.GasUsed)
				if tx.Error != "" {
					msg += " error=" + tx.Error
				}
				report.Findings[i].Evidence = append(report.Findings[i].Evidence, model.Evidence{
					Node:    s.name,
					Message: msg,
				})
			}
		}
	}
}

func updateNodeConsensusFromRPC(node *model.NodeSummary, cs rpc.ConsensusState) {
	advance := cs.Height > node.LastHeight ||
		(cs.Height == node.LastHeight && cs.Round > node.LastRound) ||
		(cs.Height == node.LastHeight && cs.Round == node.LastRound && cs.Step != "" && node.LastStep == "")

	if !advance {
		return
	}
	node.LastHeight = cs.Height
	node.LastRound = cs.Round
	if cs.Step != "" {
		node.LastStep = cs.Step
	}
	if cs.Height >= node.VoteStateHeight {
		node.VoteStateHeight = cs.Height
		node.PrevotesReceived = cs.PrevotesReceived
		node.PrevotesTotal = cs.PrevotesTotal
		node.PrevotesMaj23 = cs.PrevotesMaj23
		node.PrevotesBitArray = cs.PrevotesBitArray
		node.PrecommitsReceived = cs.PrecommitsReceived
		node.PrecommitsTotal = cs.PrecommitsTotal
		node.PrecommitsMaj23 = cs.PrecommitsMaj23
		node.PrecommitsBitArray = cs.PrecommitsBitArray
	}
}

func filterWindow(events []model.Event, since, until time.Time) []model.Event {
	if since.IsZero() && until.IsZero() {
		return events
	}
	filtered := make([]model.Event, 0, len(events))
	for _, event := range events {
		if event.HasTimestamp {
			if !since.IsZero() && event.Timestamp.Before(since) {
				continue
			}
			if !until.IsZero() && event.Timestamp.After(until) {
				continue
			}
		}
		filtered = append(filtered, event)
	}
	return filtered
}

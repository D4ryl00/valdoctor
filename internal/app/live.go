package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/D4ryl00/valdoctor/internal/api"
	"github.com/D4ryl00/valdoctor/internal/live"
	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/parse"
	"github.com/D4ryl00/valdoctor/internal/source"
	"github.com/D4ryl00/valdoctor/internal/store"
	"github.com/D4ryl00/valdoctor/internal/tui"
	"github.com/gnolang/gno/tm2/pkg/commands"
)

type liveCfg struct {
	configPath       string
	genesisPath      string
	dbPath           string
	logPaths         multiString
	validatorLogs    multiString
	sentryLogs       multiString
	dockerSources    multiString
	validatorDocker  multiString
	sentryDocker     multiString
	cmdSources       multiString
	validatorCmds    multiString
	sentryCmds       multiString
	metadataPaths    multiString
	nodeBindings     multiString
	roleBindings     multiString
	sinceRaw         string
	maxHistory       int
	propagationGrace time.Duration
	closurePolicyRaw string
	apiAddr          string
	noTUI            bool
	color            bool
	noColor          bool
}

func newLiveCmd(io commands.IO) *commands.Command {
	cfg := &liveCfg{}

	return commands.NewCommand(
		commands.Metadata{
			Name:       "live",
			ShortUsage: "live [flags]",
			ShortHelp:  "ingest running node logs in real time and keep bounded live state",
		},
		cfg,
		func(ctx context.Context, args []string) error {
			// Positional arguments are treated as additional --log paths.
			// This lets shell globs work naturally: the shell expands
			// `val*.log` into separate argv entries before flag.Parse runs,
			// and those entries land here rather than in logPaths.
			for _, arg := range args {
				if err := cfg.logPaths.Set(arg); err != nil {
					return fmt.Errorf("invalid log path %q: %w", arg, err)
				}
			}
			return execLive(ctx, cfg, io)
		},
	)
}

func (c *liveCfg) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", "", "path to a TOML config file (default: $XDG_CONFIG_HOME/valdoctor/config.toml)")
	fs.StringVar(&c.genesisPath, "genesis", "", "path to genesis.json")
	fs.StringVar(&c.dbPath, "db", "", "optional SQLite path for persistent live state")
	fs.Var(&c.logPaths, "log", "generic log file path; may be repeated")
	fs.Var(&c.validatorLogs, "validator-log", "validator log file path; may be repeated")
	fs.Var(&c.sentryLogs, "sentry-log", "sentry log file path; may be repeated")
	fs.Var(&c.dockerSources, "docker", "generic Docker container name or ID; may be repeated")
	fs.Var(&c.validatorDocker, "validator-docker", "validator Docker container name or ID; may be repeated")
	fs.Var(&c.sentryDocker, "sentry-docker", "sentry Docker container name or ID; may be repeated")
	fs.Var(&c.cmdSources, "cmd", "generic external command as <name>=<cmd> (e.g. \"val1=ssh user@host tail -f /var/log/gnoland.log\"); may be repeated")
	fs.Var(&c.validatorCmds, "validator-cmd", "validator external command as <name>=<cmd>; may be repeated")
	fs.Var(&c.sentryCmds, "sentry-cmd", "sentry external command as <name>=<cmd>; may be repeated")
	fs.Var(&c.metadataPaths, "metadata", "TOML metadata file path; may be repeated")
	fs.Var(&c.nodeBindings, "node", "bind a node name to a log path or docker source as <name>=<path|docker:container>")
	fs.Var(&c.roleBindings, "role", "assign a role to a node as <name>=<role>")
	fs.StringVar(&c.sinceRaw, "since", "", "bootstrap from lines at or after this RFC3339 timestamp")
	fs.IntVar(&c.maxHistory, "max-history", 500, "maximum number of recent heights to retain in memory")
	fs.DurationVar(&c.propagationGrace, "propagation-grace", 5*time.Second, "grace window that starts when a height closes")
	fs.StringVar(&c.closurePolicyRaw, "closure-policy", "single_validator_commit", "height closure policy: single_validator_commit, observed_validator_majority, observed_all_validator_sources")
	fs.StringVar(&c.apiAddr, "api-addr", "", "optional HTTP listen address for the live REST/WebSocket API")
	fs.BoolVar(&c.noTUI, "no-tui", false, "run headless and print closure progress to stdout")
	fs.BoolVar(&c.color, "color", false, "force color output when TUI support lands")
	fs.BoolVar(&c.noColor, "no-color", false, "disable color output when TUI support lands")
}

func execLive(ctx context.Context, cfg *liveCfg, io commands.IO) error {
	if _, err := loadConfig(cfg.configPath); err != nil {
		return err
	}

	if cfg.genesisPath == "" {
		return errors.New("missing required --genesis")
	}
	if cfg.color && cfg.noColor {
		return errors.New("--color and --no-color cannot be used together")
	}

	since, _, err := parseWindow(cfg.sinceRaw, "")
	if err != nil {
		return err
	}

	genesisPath := parse.NormalizePath(cfg.genesisPath)
	genesis, err := parse.LoadGenesis(genesisPath)
	if err != nil {
		return err
	}
	genesis.Path = genesisPath

	metadata, _, err := loadAndMergeMetadata(cfg.metadataPaths)
	if err != nil {
		return err
	}

	sources, err := buildLiveSources(cfg, metadata)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return errors.New("at least one log file is required")
	}

	closurePolicy, err := parseClosurePolicy(cfg.closurePolicyRaw)
	if err != nil {
		return err
	}

	// Build a lookup from cmd source path → command slice so we can instantiate
	// CmdSource with the right argv when iterating over resolved sources below.
	cmdByPath, err := parseCmdBindings(cfg.cmdSources, cfg.validatorCmds, cfg.sentryCmds)
	if err != nil {
		return err
	}

	// First pass: identify which sources are validators.
	// If none are explicitly identified (e.g. all provided via --log without
	// --role or metadata), promote all unknown-role sources to RoleValidator so
	// that height closure, propagation tracking, and node role display work.
	validatorSources := make([]string, 0)
	seenValidators := map[string]struct{}{}
	for _, src := range sources {
		if src.Role == model.RoleValidator {
			if _, ok := seenValidators[src.Node]; !ok {
				seenValidators[src.Node] = struct{}{}
				validatorSources = append(validatorSources, src.Node)
			}
		}
	}
	if len(validatorSources) == 0 {
		for i := range sources {
			if sources[i].Role == model.RoleUnknown {
				sources[i].Role = model.RoleValidator
			}
			if _, ok := seenValidators[sources[i].Node]; !ok {
				seenValidators[sources[i].Node] = struct{}{}
				validatorSources = append(validatorSources, sources[i].Node)
			}
		}
	}
	// Sort by genesis validator index when available, fall back to alphabetical.
	tmpResolver := &live.IdentityResolver{
		Genesis:  genesis,
		Metadata: metadata,
		Sources:  sources,
	}
	sort.SliceStable(validatorSources, func(i, j int) bool {
		return live.GenesisLess(
			tmpResolver.GenesisIndexOf(validatorSources[i]),
			tmpResolver.GenesisIndexOf(validatorSources[j]),
			validatorSources[i],
			validatorSources[j],
		)
	})

	// Second pass: create LogSource wrappers (roles are now final).
	logSources := make([]source.LogSource, 0, len(sources))
	for _, src := range sources {
		switch {
		case isDockerSourcePath(src.Path):
			logSources = append(logSources, &source.DockerSource{
				Source:    src,
				Container: dockerContainerFromSourcePath(src.Path),
				Since:     since,
			})
		case source.IsCmdSourcePath(src.Path):
			logSources = append(logSources, &source.CmdSource{
				Source: src,
				Cmd:    cmdByPath[src.Path],
			})
		default:
			logSources = append(logSources, &source.FileSource{
				Source: src,
				Since:  since,
			})
		}
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	liveStore, closeStore, err := openLiveStore(cfg)
	if err != nil {
		return err
	}
	defer closeStore()

	coord := &live.Coordinator{
		Source:   source.NewMultiSource(logSources...),
		Store:    liveStore,
		Genesis:  genesis,
		Metadata: metadata,
		Sources:  sources,
		ClosureEvaluator: live.ClosureEvaluator{
			Policy:           closurePolicy,
			ValidatorSources: validatorSources,
			GraceWindow:      cfg.propagationGrace,
		},
		MaxHistory: cfg.maxHistory,
	}

	var apiErrCh chan error
	if cfg.apiAddr != "" {
		apiErrCh = make(chan error, 1)
		server := &api.Server{
			Addr:    cfg.apiAddr,
			ChainID: genesis.ChainID,
			Store:   liveStore,
		}
		go func() {
			err := server.Run(runCtx)
			if err != nil && !errors.Is(err, context.Canceled) {
				cancel()
			}
			apiErrCh <- err
		}()
	}

	if cfg.noTUI {
		coord.OnTipAdvanced = func(height int64) {
			io.Printf("tip advanced: h%d\n", height)
		}
		coord.OnHeightClosed = func(height int64) {
			io.Printf("height closed: h%d\n", height)
		}
		coord.OnIncidentUpdate = func(card model.IncidentCard) {
			io.Printf("incident %s: %s (%s)\n", card.Status, card.ID, card.Summary)
		}
		err = coord.Run(runCtx)
	} else {
		err = tui.Run(runCtx, tui.Options{
			Store:       liveStore,
			Coordinator: coord,
			ChainID:     genesis.ChainID,
			Color:       !cfg.noColor,
		})
	}
	cancel()
	if apiErrCh != nil {
		apiErr := <-apiErrCh
		if err == nil && apiErr != nil && !errors.Is(apiErr, context.Canceled) {
			err = apiErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func openLiveStore(cfg *liveCfg) (store.Store, func(), error) {
	if strings.TrimSpace(cfg.dbPath) == "" {
		return store.NewMemoryStore(cfg.maxHistory), func() {}, nil
	}

	sqliteStore, err := store.NewSQLiteStore(parse.NormalizePath(cfg.dbPath), cfg.maxHistory)
	if err != nil {
		return nil, nil, err
	}
	return sqliteStore, func() {
		_ = sqliteStore.Close()
	}, nil
}

// parseCmdBindings extracts the command slices from the raw flag values and
// returns a map of source.CmdSourcePath(name) → []string{cmd, arg1, arg2, ...}.
// It is a pure parsing helper — role/metadata resolution is done by buildCmdSources.
func parseCmdBindings(generic, validators, sentries []string) (map[string][]string, error) {
	out := map[string][]string{}
	all := append(append(append([]string(nil), generic...), validators...), sentries...)
	for _, raw := range all {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		name, rawCmd, err := splitBinding(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cmd source %q: expected <name>=<command>", raw)
		}
		words := strings.Fields(rawCmd)
		if len(words) == 0 {
			return nil, fmt.Errorf("invalid cmd source %q: command must not be empty", raw)
		}
		out[source.CmdSourcePath(name)] = words
	}
	return out, nil
}

func parseDockerLogsCommand(words []string) (string, bool) {
	if len(words) < 3 || words[0] != "docker" || words[1] != "logs" {
		return "", false
	}

	container := ""
	for i := 2; i < len(words); i++ {
		word := words[i]
		switch {
		case word == "-f" || word == "--follow" || word == "-t" || word == "--timestamps":
			continue
		case strings.HasPrefix(word, "--tail="), strings.HasPrefix(word, "--since="):
			continue
		case word == "--tail" || word == "--since":
			i++
			if i >= len(words) || strings.TrimSpace(words[i]) == "" {
				return "", false
			}
			continue
		case strings.HasPrefix(word, "-"):
			return "", false
		case container == "":
			container = word
		default:
			return "", false
		}
	}

	return container, container != ""
}

func parseClosurePolicy(raw string) (model.ClosurePolicy, error) {
	switch strings.TrimSpace(raw) {
	case "", "single_validator_commit":
		return model.PolicySingleValidatorCommit, nil
	case "observed_validator_majority":
		return model.PolicyObservedValidatorMajority, nil
	case "observed_all_validator_sources":
		return model.PolicyObservedAllValidatorSources, nil
	default:
		return model.PolicySingleValidatorCommit, fmt.Errorf("unsupported --closure-policy %q", raw)
	}
}

func buildLiveSources(cfg *liveCfg, meta model.Metadata) ([]model.Source, error) {
	fileSources, err := buildSourcesFromParams(cfg.logPaths, cfg.validatorLogs, cfg.sentryLogs, cfg.nodeBindings, cfg.roleBindings, meta)
	if err != nil {
		return nil, err
	}

	dockerSources, err := buildDockerSources(cfg.dockerSources, cfg.validatorDocker, cfg.sentryDocker, cfg.nodeBindings, cfg.roleBindings, meta)
	if err != nil {
		return nil, err
	}

	cmdSources, err := buildCmdSources(cfg.cmdSources, cfg.validatorCmds, cfg.sentryCmds, cfg.roleBindings, meta)
	if err != nil {
		return nil, err
	}

	sources := append(fileSources, dockerSources...)
	sources = append(sources, cmdSources...)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Path < sources[j].Path
	})
	return sources, nil
}

func buildDockerSources(generic, validators, sentries, nodeBindings, roleBindings []string, meta model.Metadata) ([]model.Source, error) {
	nodeByContainer := map[string]string{}
	roleByNode := map[string]model.Role{}
	usedNames := map[string]int{}

	for name, node := range meta.Nodes {
		roleByNode[name] = model.ParseRole(node.Role)
	}

	for _, binding := range nodeBindings {
		name, rawSource, err := splitBinding(binding)
		if err != nil {
			return nil, fmt.Errorf("invalid --node value %q: %w", binding, err)
		}
		container, ok := parseDockerBinding(rawSource)
		if !ok {
			continue
		}
		nodeByContainer[container] = name
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

	type pendingDocker struct {
		container string
		role      model.Role
	}
	pending := make([]pendingDocker, 0, len(generic)+len(validators)+len(sentries))
	for _, container := range generic {
		pending = append(pending, pendingDocker{container: strings.TrimSpace(container), role: model.RoleUnknown})
	}
	for _, container := range validators {
		pending = append(pending, pendingDocker{container: strings.TrimSpace(container), role: model.RoleValidator})
	}
	for _, container := range sentries {
		pending = append(pending, pendingDocker{container: strings.TrimSpace(container), role: model.RoleSentry})
	}

	seen := map[string]model.Source{}
	for _, item := range pending {
		if item.container == "" {
			continue
		}
		path := dockerSourcePath(item.container)
		src, ok := seen[path]
		if !ok {
			nodeName := nodeByContainer[item.container]
			explicitNode := nodeName != ""
			if nodeName == "" {
				nodeName = parse.DefaultNodeName(item.container, usedNames)
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
			src = model.Source{
				Path:         path,
				Node:         nodeName,
				Role:         role,
				ExplicitNode: explicitNode,
				ExplicitRole: item.role != model.RoleUnknown,
			}
		} else if src.Role == model.RoleUnknown && item.role != model.RoleUnknown {
			src.Role = item.role
			src.ExplicitRole = true
		}
		if src.Role == model.RoleUnknown {
			if mapped, ok := roleByNode[src.Node]; ok {
				src.Role = mapped
			}
		}
		seen[path] = src
	}

	sources := make([]model.Source, 0, len(seen))
	for _, src := range seen {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Path < sources[j].Path
	})
	return sources, nil
}

// buildCmdSources resolves model.Source descriptors for external command sources.
// Each entry in generic/validators/sentries must be of the form "name=cmd arg1 arg2..."
// where everything after the first '=' is split on whitespace to form the command slice.
func buildCmdSources(generic, validators, sentries, roleBindings []string, meta model.Metadata) ([]model.Source, error) {
	roleByNode := map[string]model.Role{}
	for name, node := range meta.Nodes {
		roleByNode[name] = model.ParseRole(node.Role)
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

	type pendingCmd struct {
		raw  string
		role model.Role
	}
	pending := make([]pendingCmd, 0, len(generic)+len(validators)+len(sentries))
	for _, v := range generic {
		pending = append(pending, pendingCmd{raw: strings.TrimSpace(v), role: model.RoleUnknown})
	}
	for _, v := range validators {
		pending = append(pending, pendingCmd{raw: strings.TrimSpace(v), role: model.RoleValidator})
	}
	for _, v := range sentries {
		pending = append(pending, pendingCmd{raw: strings.TrimSpace(v), role: model.RoleSentry})
	}

	seen := map[string]model.Source{}
	for _, item := range pending {
		if item.raw == "" {
			continue
		}
		name, rawCmd, err := splitBinding(item.raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cmd source %q: expected <name>=<command>", item.raw)
		}
		if rawCmd == "" {
			return nil, fmt.Errorf("invalid cmd source %q: command must not be empty", item.raw)
		}
		words := strings.Fields(rawCmd)
		if len(words) == 0 {
			return nil, fmt.Errorf("invalid cmd source %q: command must not be empty", item.raw)
		}
		path := source.CmdSourcePath(name)
		if container, ok := parseDockerLogsCommand(words); ok {
			path = dockerSourcePath(container)
		}
		src, ok := seen[path]
		if !ok {
			role := item.role
			if role == model.RoleUnknown {
				if mapped, ok := roleByNode[name]; ok {
					role = mapped
				}
			}
			if role == model.RoleUnknown {
				if metaNode, ok := meta.Nodes[name]; ok {
					role = model.ParseRole(metaNode.Role)
				}
			}
			src = model.Source{
				Path:         path,
				Node:         name,
				Role:         role,
				ExplicitNode: true,
				ExplicitRole: item.role != model.RoleUnknown,
			}
		} else if src.Role == model.RoleUnknown && item.role != model.RoleUnknown {
			src.Role = item.role
			src.ExplicitRole = true
		}
		seen[path] = src
	}

	sources := make([]model.Source, 0, len(seen))
	for _, src := range seen {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Path < sources[j].Path
	})
	return sources, nil
}

func parseDockerBinding(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "docker:"):
		value := strings.TrimPrefix(raw, "docker:")
		return strings.TrimSpace(value), value != ""
	default:
		return "", false
	}
}

func dockerSourcePath(container string) string {
	return "docker:" + strings.TrimSpace(container)
}

func dockerContainerFromSourcePath(path string) string {
	return strings.TrimPrefix(path, "docker:")
}

func isDockerSourcePath(path string) bool {
	return strings.HasPrefix(path, "docker:")
}

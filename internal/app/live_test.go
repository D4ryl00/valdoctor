package app

import (
	"path/filepath"
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/D4ryl00/valdoctor/internal/source"
	"github.com/D4ryl00/valdoctor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBuildLiveSourcesIncludesDockerSources(t *testing.T) {
	cfg := &liveCfg{
		logPaths:        multiString{"/tmp/validator.log"},
		validatorDocker: multiString{"validator-container"},
		nodeBindings:    multiString{"validator_a=/tmp/validator.log", "validator_b=docker:validator-container"},
	}

	sources, err := buildLiveSources(cfg, model.Metadata{})
	require.NoError(t, err)
	require.Len(t, sources, 2)

	require.Equal(t, "/tmp/validator.log", sources[0].Path)
	require.Equal(t, "validator_a", sources[0].Node)
	require.Equal(t, "docker:validator-container", sources[1].Path)
	require.Equal(t, "validator_b", sources[1].Node)
	require.Equal(t, model.RoleValidator, sources[1].Role)
}

func TestParseDockerBindingRequiresPrefix(t *testing.T) {
	value, ok := parseDockerBinding("docker:validator-a")
	require.True(t, ok)
	require.Equal(t, "validator-a", value)

	_, ok = parseDockerBinding("/tmp/validator.log")
	require.False(t, ok)
}

func TestBuildLiveSourcesIncludesCmdSources(t *testing.T) {
	cfg := &liveCfg{
		validatorCmds: multiString{`val1=ssh user@server1 journalctl -f -u gnoland`},
		sentryCmds:    multiString{`sentry1=ssh user@server2 tail -f /var/log/sentry.log`},
	}

	sources, err := buildLiveSources(cfg, model.Metadata{})
	require.NoError(t, err)
	require.Len(t, sources, 2)

	byPath := map[string]model.Source{}
	for _, s := range sources {
		byPath[s.Path] = s
	}

	val1 := byPath[source.CmdSourcePath("val1")]
	require.Equal(t, "val1", val1.Node)
	require.Equal(t, model.RoleValidator, val1.Role)
	require.True(t, source.IsCmdSourcePath(val1.Path))

	s1 := byPath[source.CmdSourcePath("sentry1")]
	require.Equal(t, "sentry1", s1.Node)
	require.Equal(t, model.RoleSentry, s1.Role)
}

func TestParseCmdBindingsExtractsCommandSlices(t *testing.T) {
	cmds, err := parseCmdBindings(
		[]string{`gen=echo hello`},
		[]string{`val1=ssh user@host journalctl -f -u gnoland`},
		[]string{},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"echo", "hello"}, cmds[source.CmdSourcePath("gen")])
	require.Equal(t, []string{"ssh", "user@host", "journalctl", "-f", "-u", "gnoland"}, cmds[source.CmdSourcePath("val1")])
}

func TestParseCmdBindingsRejectsEmptyCommand(t *testing.T) {
	_, err := parseCmdBindings([]string{"val1="}, nil, nil)
	require.Error(t, err)
	// splitBinding rejects blank values before we reach the fields-check
	require.Contains(t, err.Error(), "val1=")
}

func TestParseCmdBindingsRejectsMissingName(t *testing.T) {
	_, err := parseCmdBindings([]string{"just-a-cmd-no-equals"}, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected <name>=<command>")
}

func TestOpenLiveStoreUsesSQLiteWhenDBConfigured(t *testing.T) {
	cfg := &liveCfg{
		dbPath:     filepath.Join(t.TempDir(), "live.db"),
		maxHistory: 7,
	}

	liveStore, closeStore, err := openLiveStore(cfg)
	require.NoError(t, err)
	defer closeStore()

	_, ok := liveStore.(*store.SQLiteStore)
	require.True(t, ok)
}

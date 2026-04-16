package app

import (
	"path/filepath"
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
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

package live

import (
	"testing"

	"github.com/D4ryl00/valdoctor/internal/model"
	"github.com/stretchr/testify/require"
)

func TestIdentityResolverUsesMetadataAndGenesis(t *testing.T) {
	resolver := IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "genesis-a", Address: "00112233445566778899AABBCCDDEEFF00112233"},
				{Name: "genesis-b", Address: "AABBCCDDEEFF00112233445566778899AABBCCDD"},
			},
		},
		Metadata: model.Metadata{
			Nodes: map[string]model.MetadataNode{
				"validator-a": {
					Role:             string(model.RoleValidator),
					ValidatorName:    "genesis-a",
					ValidatorAddress: "00112233445566778899AABBCCDDEEFF00112233",
				},
			},
		},
		Sources: []model.Source{
			{Node: "validator-a", Role: model.RoleValidator, ExplicitNode: true},
			{Node: "validator-b", Role: model.RoleValidator},
		},
	}

	byNode, ok := resolver.ResolveByNode("validator-a")
	require.True(t, ok)
	require.Equal(t, "validator-a", byNode.NodeName)
	require.Equal(t, "001122334455", byNode.ShortAddr)
	require.Equal(t, 0, byNode.GenesisIndex)

	byShort, ok := resolver.ResolveByShortAddr("AABBCC")
	require.True(t, ok)
	require.Equal(t, "genesis-b", byShort.NodeName)

	validators := resolver.AllTrackedValidators()
	require.Len(t, validators, 2)
	require.Equal(t, "validator-a", validators[0].NodeName)
	require.Equal(t, "validator-b", validators[1].NodeName)
}

func TestIdentityResolverUnresolvedSourceGenesisIndexIsUnknown(t *testing.T) {
	resolver := IdentityResolver{
		Sources: []model.Source{
			{Node: "docker_val1", Role: model.RoleValidator},
		},
	}

	identity, ok := resolver.ResolveByNode("docker_val1")
	require.True(t, ok)
	require.Equal(t, "docker_val1", identity.NodeName)
	require.Equal(t, -1, identity.GenesisIndex)
}

func TestIdentityResolverResolveByShortAddrIncludesGenesisIndex(t *testing.T) {
	resolver := IdentityResolver{
		Genesis: model.Genesis{
			Validators: []model.Validator{
				{Name: "genesis-a", Address: "00112233445566778899AABBCCDDEEFF00112233"},
				{Name: "genesis-b", Address: "AABBCCDDEEFF00112233445566778899AABBCCDD"},
			},
		},
	}

	identity, ok := resolver.ResolveByShortAddr("AABBCC")
	require.True(t, ok)
	require.Equal(t, "genesis-b", identity.NodeName)
	require.Equal(t, 1, identity.GenesisIndex)
}

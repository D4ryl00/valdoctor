package live

import (
	"sort"
	"strings"

	"github.com/D4ryl00/valdoctor/internal/model"
)

type IdentityResolver struct {
	Genesis  model.Genesis
	Metadata model.Metadata
	Sources  []model.Source
}

func (r *IdentityResolver) ResolveByNode(name string) (model.ValidatorIdentity, bool) {
	if name == "" {
		return model.ValidatorIdentity{}, false
	}

	if source, ok := r.findSource(name, true); ok {
		identity := r.identityForSource(source)
		if identity.NodeName != "" {
			return identity, true
		}
	}

	if metaNode, ok := r.Metadata.Nodes[name]; ok {
		identity := model.ValidatorIdentity{
			NodeName:    name,
			FullAddr:    metaNode.ValidatorAddress,
			ShortAddr:   shortAddr(metaNode.ValidatorAddress),
			IsValidator: metaNode.ValidatorAddress != "" || metaNode.ValidatorName != "" || strings.EqualFold(metaNode.Role, string(model.RoleValidator)),
		}
		if idx, ok := r.findGenesis(metaNode.ValidatorAddress, metaNode.ValidatorName); ok {
			identity.GenesisIndex = idx
			if identity.FullAddr == "" {
				identity.FullAddr = r.Genesis.Validators[idx].Address
				identity.ShortAddr = shortAddr(identity.FullAddr)
			}
			identity.IsValidator = true
		}
		if identity.NodeName != "" {
			return identity, identity.IsValidator
		}
	}

	if idx, ok := r.findGenesis("", name); ok {
		validator := r.Genesis.Validators[idx]
		return model.ValidatorIdentity{
			NodeName:     name,
			FullAddr:     validator.Address,
			ShortAddr:    shortAddr(validator.Address),
			GenesisIndex: idx,
			IsValidator:  true,
		}, true
	}

	if source, ok := r.findSource(name, false); ok {
		return model.ValidatorIdentity{
			NodeName:    source.Node,
			IsValidator: source.Role == model.RoleValidator,
		}, true
	}

	return model.ValidatorIdentity{}, false
}

func (r *IdentityResolver) ResolveByShortAddr(prefix string) (model.ValidatorIdentity, bool) {
	want := strings.ToUpper(strings.TrimSpace(prefix))
	if want == "" {
		return model.ValidatorIdentity{}, false
	}

	for _, identity := range r.AllTrackedValidators() {
		if strings.HasPrefix(identity.ShortAddr, want) {
			return identity, true
		}
	}
	for _, validator := range r.Genesis.Validators {
		short := shortAddr(validator.Address)
		if strings.HasPrefix(short, want) {
			return model.ValidatorIdentity{
				NodeName:    validator.Name,
				FullAddr:    validator.Address,
				ShortAddr:   short,
				IsValidator: true,
			}, true
		}
	}

	return model.ValidatorIdentity{}, false
}

func (r *IdentityResolver) AllTrackedValidators() []model.ValidatorIdentity {
	seen := map[string]struct{}{}
	out := make([]model.ValidatorIdentity, 0, len(r.Sources))

	for _, source := range r.Sources {
		if source.Role != model.RoleValidator {
			continue
		}
		identity := r.identityForSource(source)
		if identity.NodeName == "" {
			continue
		}
		if _, ok := seen[identity.NodeName]; ok {
			continue
		}
		seen[identity.NodeName] = struct{}{}
		out = append(out, identity)
	}

	sort.Slice(out, func(i, j int) bool {
		return GenesisLess(out[i].GenesisIndex, out[j].GenesisIndex, out[i].NodeName, out[j].NodeName)
	})
	return out
}

// GenesisIndexOf returns the genesis validator slot for the given node name,
// or -1 if the node is not in the genesis validator set.
func (r *IdentityResolver) GenesisIndexOf(name string) int {
	identity, ok := r.ResolveByNode(name)
	if ok {
		return identity.GenesisIndex
	}
	return -1
}

// GenesisLess is the canonical sort comparator: genesis-indexed nodes come
// first (ascending by index), nodes not in genesis come last (alphabetical).
func GenesisLess(ii, ji int, ni, nj string) bool {
	switch {
	case ii >= 0 && ji >= 0:
		return ii < ji
	case ii >= 0:
		return true
	case ji >= 0:
		return false
	default:
		return ni < nj
	}
}

func (r *IdentityResolver) identityForSource(source model.Source) model.ValidatorIdentity {
	identity := model.ValidatorIdentity{
		NodeName:     source.Node,
		IsValidator:  source.Role == model.RoleValidator,
		GenesisIndex: -1,
	}

	if metaNode, ok := r.Metadata.Nodes[source.Node]; ok {
		identity.FullAddr = metaNode.ValidatorAddress
		identity.ShortAddr = shortAddr(identity.FullAddr)
		if idx, ok := r.findGenesis(metaNode.ValidatorAddress, metaNode.ValidatorName); ok {
			identity.GenesisIndex = idx
			if identity.FullAddr == "" {
				identity.FullAddr = r.Genesis.Validators[idx].Address
				identity.ShortAddr = shortAddr(identity.FullAddr)
			}
			identity.IsValidator = true
		}
		// ValsetIndex explicitly declared in metadata overrides the genesis-derived
		// index. This is the escape hatch for validators added post-genesis via
		// governance proposals: their active valset slot is not in the genesis file.
		if metaNode.ValsetIndex != nil && *metaNode.ValsetIndex >= 0 {
			identity.GenesisIndex = *metaNode.ValsetIndex
			identity.IsValidator = true
		}
	}

	if identity.FullAddr == "" {
		if idx, ok := r.findGenesis("", source.Node); ok {
			identity.GenesisIndex = idx
			identity.FullAddr = r.Genesis.Validators[idx].Address
			identity.ShortAddr = shortAddr(identity.FullAddr)
			identity.IsValidator = true
		}
	}

	return identity
}

// ResolveByGenesisIndex returns the node name for a given genesis validator
// slot index (0-based). Used to map BitArray positions to origin nodes.
// Returns "" when the index is out of range or cannot be resolved.
func (r *IdentityResolver) ResolveByGenesisIndex(idx int) string {
	if idx < 0 {
		return ""
	}

	// For slots beyond the genesis validator list, check metadata ValsetIndex
	// declarations (validators added post-genesis via governance proposals).
	if idx >= len(r.Genesis.Validators) {
		for nodeName, metaNode := range r.Metadata.Nodes {
			if metaNode.ValsetIndex != nil && *metaNode.ValsetIndex == idx {
				return nodeName
			}
		}
		return ""
	}

	validator := r.Genesis.Validators[idx]

	// Try address match first (needs metadata or source with ValidatorAddress).
	if validator.Address != "" {
		for _, src := range r.Sources {
			identity := r.identityForSource(src)
			if identity.FullAddr != "" && strings.EqualFold(identity.FullAddr, validator.Address) {
				return src.Node
			}
		}
	}

	// Try genesis name match against source node names.
	if validator.Name != "" {
		for _, src := range r.Sources {
			if strings.EqualFold(src.Node, validator.Name) {
				return src.Node
			}
		}
		// Try metadata validator name.
		for nodeName, metaNode := range r.Metadata.Nodes {
			if strings.EqualFold(metaNode.ValidatorName, validator.Name) {
				return nodeName
			}
		}
		return validator.Name // best-effort: use genesis name directly
	}

	return ""
}

func (r *IdentityResolver) findSource(name string, requireExplicit bool) (model.Source, bool) {
	for _, source := range r.Sources {
		if source.Node != name {
			continue
		}
		if requireExplicit && !source.ExplicitNode {
			continue
		}
		return source, true
	}
	return model.Source{}, false
}

func (r *IdentityResolver) findGenesis(address, name string) (int, bool) {
	addrLower := strings.ToLower(strings.TrimSpace(address))
	nameLower := strings.ToLower(strings.TrimSpace(name))

	if addrLower != "" {
		for idx, validator := range r.Genesis.Validators {
			if strings.ToLower(validator.Address) == addrLower {
				return idx, true
			}
		}
	}
	if nameLower != "" {
		for idx, validator := range r.Genesis.Validators {
			if strings.ToLower(validator.Name) == nameLower {
				return idx, true
			}
		}
	}
	return 0, false
}

func shortAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	if !isHexString(addr) {
		if sep := strings.LastIndexByte(addr, '1'); sep >= 1 && sep+1 < len(addr) {
			addr = addr[sep+1:]
		}
	}

	clean := strings.ToUpper(addr)
	if len(clean) > 12 {
		return clean[:12]
	}
	return clean
}

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

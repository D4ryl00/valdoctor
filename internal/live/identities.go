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
		return out[i].NodeName < out[j].NodeName
	})
	return out
}

func (r *IdentityResolver) identityForSource(source model.Source) model.ValidatorIdentity {
	identity := model.ValidatorIdentity{
		NodeName:    source.Node,
		IsValidator: source.Role == model.RoleValidator,
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

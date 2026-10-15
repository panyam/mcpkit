package agentsclient

import "github.com/panyam/mcpkit/experimental/ext/agents"

// Host-side resolution of agents/get's modelRoutingHint (agents-wg#22).
//
// The hint is advisory. Its role is a relative class (small, medium, large),
// and the host maps it to models it has under its own policy. When the hint
// is absent, unsupported or cannot be satisfied, the host may use its default
// model. RoleTable is that mapping, generic in the host's model type so this
// package never needs to know what a model is.

// roleOrder lists roles from least to most capable. Resolution only ever
// moves up it.
var roleOrder = []agents.ModelRole{agents.ModelRoleSmall, agents.ModelRoleMedium, agents.ModelRoleLarge}

// RoleModel is the model a host runs for one role, and whether it accepts
// images.
type RoleModel[M any] struct {
	Model  M
	Vision bool
}

// RoleTable maps model roles to a host's models. A role with no entry is
// one the host has no model for.
type RoleTable[M any] struct {
	Roles map[agents.ModelRole]RoleModel[M]

	// Default is used when the hint is absent, names a role this package
	// does not know, or cannot be satisfied.
	Default M
}

// Resolution says how Resolve chose its model.
type Resolution int

const (
	// ResolvedDefault means the hint was absent, unknown or unsatisfiable,
	// and Default was used.
	ResolvedDefault Resolution = iota
	// ResolvedHinted means the hinted role's own model was used.
	ResolvedHinted
	// ResolvedEscalated means a larger role's model was used, because the
	// hinted role had no model or its model lacks vision the hint asked for.
	ResolvedEscalated
)

// String returns "default", "hinted" or "escalated".
func (r Resolution) String() string {
	switch r {
	case ResolvedHinted:
		return "hinted"
	case ResolvedEscalated:
		return "escalated"
	default:
		return "default"
	}
}

// Resolve picks a model for an agent from its hint. It takes the hinted
// role's model if there is one and, when the hint asks for vision, that
// model has it. Otherwise it tries each larger role in turn, and never a
// smaller one, since a smaller model is a capability downgrade the server
// did not ask for. A hint with no role starts from small. When nothing fits,
// it returns Default.
func (t RoleTable[M]) Resolve(h *agents.ModelRoutingHint) (M, Resolution) {
	if h == nil {
		return t.Default, ResolvedDefault
	}
	start := 0
	if h.Role != "" {
		start = -1
		for i, r := range roleOrder {
			if r == h.Role {
				start = i
				break
			}
		}
		if start < 0 {
			return t.Default, ResolvedDefault
		}
	}
	for i := start; i < len(roleOrder); i++ {
		m, ok := t.Roles[roleOrder[i]]
		if !ok || (h.Vision && !m.Vision) {
			continue
		}
		if i == start && h.Role != "" {
			return m.Model, ResolvedHinted
		}
		return m.Model, ResolvedEscalated
	}
	return t.Default, ResolvedDefault
}

// LargestRole merges the hints of several agents a host runs together, using
// the policy the SEP gives as an example: the largest declared role, and
// vision if any hint asks for it. Nil hints are skipped, and the result is
// nil when every hint is. Merging is host-defined, so this is one choice
// among several.
func LargestRole(hints ...*agents.ModelRoutingHint) *agents.ModelRoutingHint {
	var out *agents.ModelRoutingHint
	best := -1
	for _, h := range hints {
		if h == nil {
			continue
		}
		if out == nil {
			out = &agents.ModelRoutingHint{}
		}
		out.Vision = out.Vision || h.Vision
		for i, r := range roleOrder {
			if r == h.Role && i > best {
				best = i
				out.Role = r
			}
		}
	}
	return out
}

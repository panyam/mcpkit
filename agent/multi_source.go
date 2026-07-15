package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// ToolOwner identifies one source's claim on a tool name during collision
// resolution.
type ToolOwner struct {
	// SourceID is the identifier the source was added under.
	SourceID string
	// Def is that source's definition for the contested name.
	Def core.ToolDef
}

// Resolver picks which source handles an ambiguous bare-name call. Returning
// an empty string (or an error) fails the call; the caller can always reach a
// specific candidate via the qualified "sourceID_name" form instead.
type Resolver func(name string, candidates []ToolOwner, args map[string]any) (sourceID string, err error)

// MultiSource aggregates ToolSources under stable source IDs, mirroring the
// collision semantics of ext/ui's ServerRegistry in ToolSource form:
//
//   - Unique names are exposed and callable as-is.
//   - Colliding names are exposed to the model ONLY in qualified form
//     ("sourceID_name" for every claimant), so the model-facing list never
//     contains duplicates and every tool stays reachable.
//   - A bare-name Call that hits a collision consults the Resolver if one is
//     configured, else fails with an error naming the qualified forms.
//
// Qualification is deterministic: it depends only on the set of source IDs
// claiming the name, not on registration order.
type MultiSource struct {
	mu          sync.RWMutex
	order       []string
	sources     map[string]ToolSource
	resolver    Resolver
	onCollision func(name string, sourceIDs []string)
}

// MultiOption configures a MultiSource.
type MultiOption func(*MultiSource)

// WithResolver installs the ambiguous-call resolver used for bare-name calls
// that collide.
func WithResolver(r Resolver) MultiOption {
	return func(m *MultiSource) { m.resolver = r }
}

// WithCollisionNotify installs a hook invoked (synchronously, under the
// source lock) whenever Tools discovers a name claimed by multiple sources.
// Intended for logging/metrics, not control flow.
func WithCollisionNotify(fn func(name string, sourceIDs []string)) MultiOption {
	return func(m *MultiSource) { m.onCollision = fn }
}

// NewMultiSource returns an empty aggregator.
func NewMultiSource(opts ...MultiOption) *MultiSource {
	m := &MultiSource{sources: make(map[string]ToolSource)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Add registers a source under id. IDs must be unique and must not contain
// "_", which is reserved as the qualified-name separator; rejecting it keeps
// "sourceID_name" parsing unambiguous.
func (m *MultiSource) Add(id string, src ToolSource) error {
	if strings.Contains(id, "_") {
		return fmt.Errorf("agent: source id %q must not contain underscores", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sources[id]; exists {
		return fmt.Errorf("agent: source %q already added", id)
	}
	m.sources[id] = src
	m.order = append(m.order, id)
	return nil
}

// Remove drops a source. Unknown ids are a no-op.
func (m *MultiSource) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sources[id]; !ok {
		return
	}
	delete(m.sources, id)
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

// Tools returns the merged, deduplicated model-facing list: unique names
// as-is (source order preserved), collisions replaced by qualified names for
// every claimant.
func (m *MultiSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	claims, orderNames, err := m.gatherLocked(ctx)
	if err != nil {
		return nil, err
	}

	var out []core.ToolDef
	for _, name := range orderNames {
		owners := claims[name]
		if len(owners) == 1 {
			out = append(out, owners[0].Def)
			continue
		}
		if m.onCollision != nil {
			ids := make([]string, len(owners))
			for i, o := range owners {
				ids[i] = o.SourceID
			}
			m.onCollision(name, ids)
		}
		for _, o := range owners {
			def := o.Def
			def.Name = qualifiedName(o.SourceID, name)
			out = append(out, def)
		}
	}
	return out, nil
}

// Call dispatches by bare or qualified name. Resolution order: exact unique
// bare name; qualified "sourceID_name"; ambiguous bare name via Resolver.
func (m *MultiSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	m.mu.RLock()
	claims, _, err := m.gatherLocked(ctx)
	if err != nil {
		m.mu.RUnlock()
		return nil, err
	}
	src, bare, resolveErr := m.resolveLocked(claims, name, args)
	m.mu.RUnlock()
	if resolveErr != nil {
		return nil, resolveErr
	}
	return src.Call(ctx, bare, args)
}

func (m *MultiSource) resolveLocked(claims map[string][]ToolOwner, name string, args map[string]any) (ToolSource, string, error) {
	if owners, ok := claims[name]; ok {
		if len(owners) == 1 {
			return m.sources[owners[0].SourceID], name, nil
		}
		if m.resolver != nil {
			id, err := m.resolver(name, owners, args)
			if err != nil {
				return nil, "", err
			}
			if src, ok := m.sources[id]; ok {
				return src, name, nil
			}
			return nil, "", fmt.Errorf("agent: resolver returned unknown source %q for tool %q", id, name)
		}
		var forms []string
		for _, o := range owners {
			forms = append(forms, qualifiedName(o.SourceID, name))
		}
		return nil, "", fmt.Errorf("agent: tool %q is ambiguous; use one of: %s", name, strings.Join(forms, ", "))
	}

	if id, bare, ok := splitQualified(name); ok {
		if src, exists := m.sources[id]; exists {
			for _, o := range claims[bare] {
				if o.SourceID == id {
					return src, bare, nil
				}
			}
		}
	}
	return nil, "", fmt.Errorf("agent: unknown tool %q", name)
}

func (m *MultiSource) gatherLocked(ctx context.Context) (map[string][]ToolOwner, []string, error) {
	claims := make(map[string][]ToolOwner)
	var orderNames []string
	for _, id := range m.order {
		defs, err := m.sources[id].Tools(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("agent: listing tools from source %q: %w", id, err)
		}
		for _, def := range defs {
			if _, seen := claims[def.Name]; !seen {
				orderNames = append(orderNames, def.Name)
			}
			claims[def.Name] = append(claims[def.Name], ToolOwner{SourceID: id, Def: def})
		}
	}
	for _, owners := range claims {
		sort.Slice(owners, func(i, j int) bool { return owners[i].SourceID < owners[j].SourceID })
	}
	return claims, orderNames, nil
}

func qualifiedName(sourceID, name string) string {
	return sourceID + "_" + name
}

func splitQualified(name string) (sourceID, bare string, ok bool) {
	i := strings.Index(name, "_")
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

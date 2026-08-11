package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
)

// delegateArgs is the {task} shape a server-advertised agent's delegate tool
// advertises to the supervisor model. It matches the default AgentSource input
// schema, so the supervisor's args pass straight through to the child.
type delegateArgs struct {
	Task string `json:"task"`
}

// delegateSchema is the delegate tool's input schema, built once.
var delegateSchema = mustSchema[delegateArgs]()

func mustSchema[T any]() any {
	var s any
	if err := json.Unmarshal(core.GenerateSchema[T](), &s); err != nil {
		panic(fmt.Sprintf("host: schema for delegate args: %v", err))
	}
	return s
}

// registerServerAgents discovers each connected server's advertised agent
// roster (experimental/ext/agents) and adds a lazy delegate source per server
// to the main aggregate, so the supervisor model can delegate to a
// server-advertised agent exactly like any other sub-agent.
//
// Progressive disclosure is the point: only the roster (agents/list — routing
// fields, no tool schemas) is pulled at boot. An agent's instructions + scoped
// tools (agents/get) resolve on first delegation, so a large roster never
// bloats the supervisor's context. This mirrors catalog-mode skills.
//
// Per-server failures degrade to a warning (never a boot failure): a server
// that is unreachable, does not speak the extension, or has an empty roster is
// skipped. Discovery runs over the servers connected at boot; a server that
// connects later does not retroactively contribute a roster (a documented
// follow-up, matching the static membership of config-declared sub-agents).
func (a *App) registerServerAgents(multi *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider, servers []ServerConfig, clientByID map[string]*client.Client) {
	for _, sc := range servers {
		if sc.Agents != nil && !*sc.Agents {
			continue
		}
		c := clientByID[sc.ID]
		if c == nil {
			continue
		}
		ac := agentsclient.New(c)
		if !ac.SupportsAgents() {
			continue
		}
		roster, err := ac.ListAgents(context.Background())
		if err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("discover agents on %s: %v", sc.ID, err)})
			continue
		}
		if len(roster) == 0 {
			continue
		}
		src := newServerAgentSource(serverAgentDeps{
			summaries: roster,
			get:       ac.GetAgent,
			backing:   agent.NewClientSource(c),
			provider:  provider,
			maxSteps:  a.cfg.MaxSteps,
			tp:        tp,
			mp:        mp,
			onEvent:   func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
		})
		if err := multi.Add("serveragents:"+sc.ID, src); err != nil {
			a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("register agents for %s: %v", sc.ID, err)})
			continue
		}
		a.emit(HostEvent{Kind: HostMessage, Message: fmt.Sprintf("server %s advertises %d delegatable agent(s)", sc.ID, len(roster))})
	}
	multi.Invalidate()
}

// serverAgentDeps bundles what one server's lazy delegate source needs.
type serverAgentDeps struct {
	summaries []agents.AgentSummary
	get       func(ctx context.Context, agentID string) (agents.AgentDetail, error)
	backing   agent.ToolSource
	provider  agent.Provider
	maxSteps  int
	tp        core.TracerProvider
	mp        core.MeterProvider
	onEvent   func(agent.SubAgentEvent)
}

// serverAgentSource is the lazy delegate ToolSource for one server's advertised
// agents. It advertises one delegate tool per roster entry (from agents/list)
// and resolves an agent's definition (agents/get) into a Runner-backed
// agent.AgentSource on first delegation, caching it thereafter. It is the host
// half of the issue-1144 bridge; agent.NewServerAgentSource is the adapter.
type serverAgentSource struct {
	deps   serverAgentDeps
	defs   []core.ToolDef                 // one delegate tool per agent
	byTool map[string]agents.AgentSummary // delegate tool name -> summary

	mu    sync.Mutex
	built map[string]*agent.AgentSource // agentID -> resolved child (cache)
}

func newServerAgentSource(deps serverAgentDeps) *serverAgentSource {
	if deps.tp == nil {
		deps.tp = core.NoopTracerProvider{}
	}
	s := &serverAgentSource{
		deps:   deps,
		byTool: make(map[string]agents.AgentSummary, len(deps.summaries)),
		built:  make(map[string]*agent.AgentSource, len(deps.summaries)),
	}
	for _, sum := range deps.summaries {
		// AgentID (unique within a server, per the extension) is the delegate
		// tool name, so names never collide within this source. DelegateTool is
		// the WG's server-side-execution entry point and stays advisory here —
		// this is the local-build path.
		name := sum.AgentID
		if _, dup := s.byTool[name]; dup {
			continue
		}
		s.defs = append(s.defs, core.ToolDef{Name: name, Description: describeAgent(sum), InputSchema: delegateSchema})
		s.byTool[name] = sum
	}
	return s
}

// describeAgent folds a roster entry's routing fields (description + capability
// labels + example tasks) into the delegate tool's description, so the
// supervisor model can route without the agent's tool schemas.
func describeAgent(sum agents.AgentSummary) string {
	var b strings.Builder
	b.WriteString(sum.Description)
	if len(sum.Capabilities) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Capabilities: " + strings.Join(sum.Capabilities, ", ") + ".")
	}
	if len(sum.ExampleTasks) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Example tasks: " + strings.Join(sum.ExampleTasks, "; ") + ".")
	}
	return b.String()
}

// Tools returns one delegate tool per advertised agent (a snapshot copy). No
// agents/get is issued — the scoped schemas stay out of the supervisor's view
// until the agent is actually delegated to.
func (s *serverAgentSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call resolves the named agent (agents/get on first call, cached after) and
// runs it. An unknown delegate name is ErrUnknownTool (a dispatch miss the
// aggregate can qualify); a resolution failure is a model-visible IsError
// result so the supervisor's turn continues, mirroring how AgentSource turns a
// child failure into an IsError result rather than aborting the turn.
func (s *serverAgentSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	summary, ok := s.byTool[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", agent.ErrUnknownTool, name)
	}
	child, err := s.resolve(ctx, summary)
	if err != nil {
		return &core.ToolResult{
			IsError: true,
			Content: []core.Content{{Type: "text", Text: fmt.Sprintf("could not resolve agent %q: %v", summary.AgentID, err)}},
		}, nil
	}
	return child.Call(ctx, name, args)
}

// resolve returns the cached child AgentSource for an agent, building it from
// an agents/get detail on first use. The agents/get call runs OUTSIDE the lock
// so concurrent delegations to different agents don't serialize on the network;
// a lost race (two calls resolving the same agent) rebuilds harmlessly and the
// first stored wins.
func (s *serverAgentSource) resolve(ctx context.Context, summary agents.AgentSummary) (*agent.AgentSource, error) {
	id := summary.AgentID
	s.mu.Lock()
	if a, ok := s.built[id]; ok {
		s.mu.Unlock()
		return a, nil
	}
	s.mu.Unlock()

	// One span per first-use resolution ties the supervisor's delegation to the
	// agents/get discovery span (on the server) and the child Runner's turn
	// spans, so a trace reads supervisor -> resolve(agent.id) -> get -> child.
	// Cache hits (the common case after warm-up) are not spanned.
	ctx, span := s.deps.tp.StartSpan(ctx, "agents.resolve", core.Attribute{Key: "mcp.agent.id", Value: id})
	defer span.End()

	detail, err := s.deps.get(ctx, id)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	child, err := agent.NewServerAgentSource(agent.ServerAgentConfig{
		Name:           summary.AgentID,
		Description:    describeAgent(summary),
		Instructions:   detail.Instructions,
		Tools:          detail.Tools,
		Backing:        s.deps.backing,
		Provider:       s.deps.provider,
		MaxSteps:       s.deps.maxSteps,
		OnEvent:        s.deps.onEvent,
		TracerProvider: s.deps.tp,
		MeterProvider:  s.deps.mp,
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if a, ok := s.built[id]; ok {
		s.mu.Unlock()
		return a, nil
	}
	s.built[id] = child
	s.mu.Unlock()
	return child, nil
}

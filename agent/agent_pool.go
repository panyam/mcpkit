package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// Runner-control tool names (issue 1166), kept fixed like the memory/meta-tool
// names.
const (
	SpawnAgentToolName  = "spawn_agent"
	AwaitAgentToolName  = "await_agent"
	CancelAgentToolName = "cancel_agent"
	ListAgentsToolName  = "list_agents"
)

type spawnArgs struct {
	// Name is the agent to spawn (one of the pool's registered names).
	Name string `json:"name"`
	// Task is the instruction handed to the spawned agent as its user turn.
	Task string `json:"task"`
}

type agentIDArgs struct {
	// ID is the handle returned by spawn_agent.
	ID string `json:"id"`
}

// NewSpawnSource returns a leaf ToolSource exposing the runner-control tools
// over pool: spawn_agent / await_agent / cancel_agent / list_agents (issue
// 1166). "Supervision = a Runner whose tools control other Runners" — this is
// that tool surface. Model-facing, so it lives in agent/ (A6). The spawn tool's
// description enumerates the pool's registered agents, so populate the pool
// (Register) before calling this.
func NewSpawnSource(pool *AgentPool) *FuncSource {
	fs := NewFuncSource()

	var menu strings.Builder
	for _, a := range pool.Registered() {
		fmt.Fprintf(&menu, "\n- %s: %s", a.Name, a.Description)
	}
	_ = AddFunc(fs, SpawnAgentToolName,
		"Start a sub-agent running in the background and get a handle id back; it keeps running while you do other work. Await its result later with await_agent, or drop it with cancel_agent. Available agents:"+menu.String(),
		func(ctx context.Context, in spawnArgs) (string, error) {
			if strings.TrimSpace(in.Task) == "" {
				return "", fmt.Errorf("spawn_agent requires a non-empty 'task'")
			}
			id, err := pool.spawn(ctx, in.Name, in.Task)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("spawned %q as %s (running in the background)", in.Name, id), nil
		})

	_ = AddFunc(fs, AwaitAgentToolName,
		"Wait for a spawned sub-agent to finish and return its result. Blocks until it completes.",
		func(ctx context.Context, in agentIDArgs) (string, error) {
			result, err := pool.await(ctx, in.ID)
			if err != nil {
				return "", err
			}
			if result == nil {
				return fmt.Sprintf("spawned agent %s finished with no result", in.ID), nil
			}
			if result.Structured.Len() > 0 {
				return string(result.Structured.Raw()), nil
			}
			return result.Text, nil
		})

	_ = AddFunc(fs, CancelAgentToolName,
		"Cancel a spawned sub-agent you no longer need, by its handle id.",
		func(ctx context.Context, in agentIDArgs) (string, error) {
			if err := pool.cancel(in.ID); err != nil {
				return "", err
			}
			return "cancelled " + in.ID, nil
		})

	_ = AddFunc(fs, ListAgentsToolName,
		"List the sub-agents you have spawned and their status (running / done / failed / cancelled).",
		func(ctx context.Context, _ struct{}) (string, error) {
			st := pool.statuses()
			if len(st) == 0 {
				return "no spawned agents", nil
			}
			var b strings.Builder
			for _, s := range st {
				fmt.Fprintf(&b, "%s (%s): %s\n", s.ID, s.Name, s.Status)
			}
			return b.String(), nil
		})

	return fs
}

// AgentPool runs named child agents in the background and hands the parent
// model a handle to each, so the model can steer sub-agents through ordinary
// tool calls (issue 1166, piece B of the 1036 control axis): spawn one now,
// await its result at a chosen point, or cancel it. It is the model-driven,
// handle-based counterpart to AgentSource (blocking, no handle) and
// AsyncAgentSource (fire-and-inject, no handle): here the model holds the
// handle and pulls the result via await, or drops it via cancel.
//
// A handle outlives the spawning turn (the child runs on a DetachForBackground
// context), so spawn-in-turn-1 / await-in-turn-3 works. Delivery is pull-only:
// a spawned result is stored on the handle and returned by await, never
// auto-injected — auto-injection is AsyncAgentSource's model. The pool is
// safe for concurrent use.
type AgentPool struct {
	mu      sync.Mutex
	agents  map[string]pooledAgent
	order   []string
	handles map[string]*spawnHandle
	seq     int
	onEvent func(SubAgentEvent)
}

type pooledAgent struct {
	runner      *Runner
	description string
	maxDepth    int
}

// spawnHandle tracks one background child. done closes exactly once, when the
// child's Run returns; result/err are then readable. cancel aborts the child.
type spawnHandle struct {
	id     string
	name   string
	done   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	status string // "running" | "done" | "failed" | "cancelled"
	result *TurnResult
	err    error
}

// SpawnStatus is a wire-serializable snapshot of one live handle for
// list_agents (constraint A2). It is a point-in-time copy: a running child may
// have finished by the time the caller reads it.
//
// It describes handles only. For the agents a pool can spawn, before any
// spawn has happened, see Registered.
type SpawnStatus struct {
	// ID is the handle the model passes to await_agent and cancel_agent.
	ID string `json:"id"`

	// Name is the registered agent this handle was spawned from.
	Name string `json:"name"`

	// Status is one of "running", "done", "failed", or "cancelled". The two
	// failure values are distinct on purpose: "failed" means the child's own
	// Run returned an error, "cancelled" means the parent dropped it, and
	// collapsing them loses that.
	Status string `json:"status"`
}

// RegisteredAgent describes one agent a pool can spawn, as returned by
// Registered. It is the pre-spawn view: no handle exists yet, so there is no
// ID and no lifecycle state.
type RegisteredAgent struct {
	// Name is the name Register was called with, and the name the model
	// passes to spawn_agent.
	Name string `json:"name"`

	// Description is the human-readable blurb Register was given, used to
	// build the spawn tool's menu so the model can choose between agents.
	Description string `json:"description"`
}

// NewAgentPool returns an empty pool. onEvent, when non-nil, receives every
// spawned child's event stream wrapped in a SubAgentEvent (scope = the agent
// name, depth threaded), so a surface renders background activity the same way
// it renders a blocking sub-agent's. Nil drops child events.
func NewAgentPool(onEvent func(SubAgentEvent)) *AgentPool {
	return &AgentPool{
		agents:  map[string]pooledAgent{},
		handles: map[string]*spawnHandle{},
		onEvent: onEvent,
	}
}

// Register adds a spawnable agent under name (its child Runner + a description
// for the model + a depth cap; zero uses DefaultMaxAgentDepth). A duplicate
// name is an error.
func (p *AgentPool) Register(name, description string, runner *Runner, maxDepth int) error {
	if name == "" || runner == nil {
		return fmt.Errorf("agent: AgentPool.Register requires a name and runner")
	}
	if maxDepth <= 0 {
		maxDepth = DefaultMaxAgentDepth
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.agents[name]; exists {
		return fmt.Errorf("agent: AgentPool already has %q", name)
	}
	p.agents[name] = pooledAgent{runner: runner, description: description, maxDepth: maxDepth}
	p.order = append(p.order, name)
	return nil
}

// Registered lists the agents this pool can spawn, in registration order,
// for a spawn tool's help text. It describes registrations rather than live
// children; use list_agents (backed by statuses) for handles in flight.
func (p *AgentPool) Registered() []RegisteredAgent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]RegisteredAgent, 0, len(p.order))
	for _, n := range p.order {
		out = append(out, RegisteredAgent{Name: n, Description: p.agents[n].description})
	}
	return out
}

// spawn starts the named agent on a detached, cancellable goroutine over an
// isolated slice seeded with task, and returns a handle id. Depth and the
// ctx-threaded call budget are checked before the spawn (an unknown name or an
// exhausted guard is an error, not a started agent). The child outlives ctx
// (DetachForBackground), so it keeps running after the spawning turn ends.
func (p *AgentPool) spawn(ctx context.Context, name, task string) (string, error) {
	p.mu.Lock()
	spec, ok := p.agents[name]
	if !ok {
		p.mu.Unlock()
		return "", fmt.Errorf("no spawnable agent %q", name)
	}
	depth := agentDepth(ctx)
	if depth >= spec.maxDepth {
		p.mu.Unlock()
		return "", fmt.Errorf("spawn %q refused: max depth %d reached", name, spec.maxDepth)
	}
	if b := agentCallBudget(ctx); b != nil && b.Add(-1) < 0 {
		p.mu.Unlock()
		return "", fmt.Errorf("spawn %q refused: call budget exhausted", name)
	}
	p.seq++
	id := fmt.Sprintf("agent-%d", p.seq)
	p.mu.Unlock()

	childScope := agentScope(ctx).Child(name)
	bgCtx := withAgentScope(withAgentDepth(core.DetachForBackground(ctx), depth+1), childScope)
	runCtx, cancel := context.WithCancel(bgCtx)

	h := &spawnHandle{id: id, name: name, done: make(chan struct{}), cancel: cancel, status: "running"}
	p.mu.Lock()
	p.handles[id] = h
	p.mu.Unlock()

	emit := func(Event) {}
	if p.onEvent != nil {
		childDepth := depth + 1
		emit = func(e Event) { p.onEvent(SubAgentEvent{Scope: childScope.String(), Depth: childDepth, Event: e}) }
	}
	go func() {
		result, err := spec.runner.Run(runCtx, []Message{{Role: RoleUser, Text: task}}, emit)
		h.finish(result, err)
	}()
	return id, nil
}

// finish records the terminal outcome and closes done exactly once. A cancelled
// handle keeps its "cancelled" status (the run error is the cancellation).
func (h *spawnHandle) finish(result *TurnResult, err error) {
	h.mu.Lock()
	if h.status == "running" {
		if err != nil {
			h.status = "failed"
		} else {
			h.status = "done"
		}
	}
	h.result, h.err = result, err
	h.mu.Unlock()
	close(h.done)
}

// await blocks until the spawned agent id finishes and returns its result, or
// returns when ctx is cancelled (the turn was cancelled). An unknown id is an
// error. await is idempotent: a second call returns the same stored result.
func (p *AgentPool) await(ctx context.Context, id string) (*TurnResult, error) {
	p.mu.Lock()
	h, ok := p.handles[id]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no spawned agent %q", id)
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, fmt.Errorf("spawned agent %q (%s) failed: %w", id, h.name, h.err)
	}
	return h.result, nil
}

// cancel aborts the spawned agent id (its context is cancelled; the running
// child returns and its handle moves to "cancelled"). An unknown id is an
// error. Cancelling a finished agent is a no-op that still succeeds.
func (p *AgentPool) cancel(id string) error {
	p.mu.Lock()
	h, ok := p.handles[id]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no spawned agent %q", id)
	}
	h.mu.Lock()
	if h.status == "running" {
		h.status = "cancelled"
	}
	h.mu.Unlock()
	h.cancel()
	return nil
}

// statuses snapshots every handle (running and finished) for list_agents,
// in id order of creation.
func (p *AgentPool) statuses() []SpawnStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]SpawnStatus, 0, len(p.handles))
	for i := 1; i <= p.seq; i++ {
		h, ok := p.handles[fmt.Sprintf("agent-%d", i)]
		if !ok {
			continue
		}
		h.mu.Lock()
		out = append(out, SpawnStatus{ID: h.id, Name: h.name, Status: h.status})
		h.mu.Unlock()
	}
	return out
}

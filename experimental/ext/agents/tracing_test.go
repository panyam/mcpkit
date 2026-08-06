package agents_test

import (
	"context"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recSpan / recTP are a local recording TracerProvider — the same dep-free
// shape events/webhook_span_test.go uses — so these tests assert the
// discovery spans without pulling in ext/otel.
type recSpan struct {
	name  string
	attrs map[string]string
	ended bool
}

func (s *recSpan) End()                     { s.ended = true }
func (s *recSpan) SetAttribute(k, v string) { s.attrs[k] = v }
func (s *recSpan) RecordError(error)        {}
func (s *recSpan) AddLink(core.Link)        {}

type recTP struct {
	mu    sync.Mutex
	spans []*recSpan
}

func (p *recTP) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &recSpan{name: name, attrs: make(map[string]string, len(attrs))}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.mu.Lock()
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	return core.WithActiveSpan(ctx, sp), sp
}

func (p *recTP) find(name string) *recSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sp := range p.spans {
		if sp.name == name {
			return sp
		}
	}
	return nil
}

// newTracedAgentsServer mirrors newAgentsServer but wires a recording
// TracerProvider so tests can inspect the discovery spans.
func newTracedAgentsServer(t *testing.T, tp core.TracerProvider, defs ...agents.AgentDef) *testutil.TestClient {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-trace-test", Version: "0.0.1"})
	_, err := agents.Register(agents.Config{Server: srv, Agents: defs, TracerProvider: tp})
	require.NoError(t, err)
	return testutil.NewTestClient(t, srv)
}

// TestListEmitsSpan asserts agents/list emits an agents.list span carrying the
// roster size, so an operator can see discovery traffic and its cardinality.
func TestListEmitsSpan(t *testing.T) {
	tp := &recTP{}
	tc := newTracedAgentsServer(t, tp, workflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)

	sp := tp.find("agents.list")
	require.NotNil(t, sp, "agents/list must emit an agents.list span")
	assert.True(t, sp.ended, "span must be ended")
	assert.Equal(t, "1", sp.attrs["agents.count"])
}

// TestGetEmitsSpanFound asserts a resolvable agents/get emits an agents.get
// span tagged with the agent id and found=true.
func TestGetEmitsSpanFound(t *testing.T) {
	tp := &recTP{}
	tc := newTracedAgentsServer(t, tp, workflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "workflow-agent"})
	require.NoError(t, err)

	sp := tp.find("agents.get")
	require.NotNil(t, sp, "agents/get must emit an agents.get span")
	assert.True(t, sp.ended)
	assert.Equal(t, "workflow-agent", sp.attrs["mcp.agent.id"])
	assert.Equal(t, "true", sp.attrs["agents.found"])
}

// TestGetEmitsSpanNotFound asserts an unknown agents/get still emits the span
// with found=false, so failed lookups are visible in traces (not silent).
func TestGetEmitsSpanNotFound(t *testing.T) {
	tp := &recTP{}
	tc := newTracedAgentsServer(t, tp, workflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "no-such-agent"})
	require.Error(t, err, "unknown agentId is a client error")

	sp := tp.find("agents.get")
	require.NotNil(t, sp)
	assert.Equal(t, "no-such-agent", sp.attrs["mcp.agent.id"])
	assert.Equal(t, "false", sp.attrs["agents.found"])
}

// TestNoTracerProviderNoPanic confirms the default (nil TracerProvider) path
// serves discovery without spans and without panicking — the zero-overhead
// opt-out contract.
func TestNoTracerProviderNoPanic(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-notrace", Version: "0.0.1"})
	_, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{workflowAgent()}})
	require.NoError(t, err)
	tc := testutil.NewTestClient(t, srv)

	_, err = tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	_, err = tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "workflow-agent"})
	require.NoError(t, err)
}

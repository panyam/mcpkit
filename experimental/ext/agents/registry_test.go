package agents_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopTool(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
	return core.TextResult("ok"), nil
}

// catalog is the server tool set every test registers. upload_report carries
// an x-mcp-file property so the fileInputs strip has something to remove.
var catalog = []core.ToolDef{
	{
		Name:        "list_failed_pipelines",
		Description: "List failed pipelines for a service.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"service": map[string]any{"type": "string"}},
			"required":   []any{"service"},
		},
	},
	{Name: "approve_gate", Description: "Approve a pending gate", InputSchema: map[string]any{"type": "object"}},
	{
		Name:        "upload_report",
		Description: "Attach a report",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"report": map[string]any{"type": "string", "format": "uri", core.FileInputSchemaKey: map[string]any{"accept": []any{"application/pdf"}}},
			},
		},
	},
}

// sepWorkflowAgent is the agent in agents-wg#22's agents/get example.
func sepWorkflowAgent() agents.AgentDef {
	return agents.AgentDef{
		Name:             "workflow-agent",
		Description:      "Investigates delivery pipelines and approvals",
		Capabilities:     []string{"pipeline failures", "deployment readiness", "approval status"},
		Instructions:     "Handle delivery readiness and approval workflows.",
		ModelRoutingHint: &agents.ModelRoutingHint{Role: agents.ModelRoleMedium},
		Tools:            []string{"list_failed_pipelines"},
	}
}

func opsAgent() agents.AgentDef {
	return agents.AgentDef{
		Name:         "ops-agent",
		Description:  "Approvals and reports",
		Capabilities: []string{"approvals"},
		Tools:        []string{"approve_gate", "upload_report", "list_failed_pipelines"},
	}
}

func newCatalogServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	for _, td := range catalog {
		srv.RegisterTool(td, noopTool)
	}
	return srv
}

func newAgentsServerWith(t *testing.T, cfg agents.Config, defs ...agents.AgentDef) (*testutil.TestClient, *agents.Registry) {
	t.Helper()
	cfg.Server = newCatalogServer()
	cfg.Agents = defs
	reg, err := agents.Register(cfg)
	require.NoError(t, err)
	return testutil.NewTestClient(t, cfg.Server), reg
}

func newAgentsServer(t *testing.T, defs ...agents.AgentDef) (*testutil.TestClient, *agents.Registry) {
	t.Helper()
	return newAgentsServerWith(t, agents.Config{}, defs...)
}

func listAll(t *testing.T, call func(method string, params any) (*client.CallResult, error)) agents.ListAgentsResult {
	t.Helper()
	res, err := call(agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListAgentsResult
	require.NoError(t, res.Unmarshal(&out))
	return out
}

func rpcError(t *testing.T, err error) *client.RPCError {
	t.Helper()
	require.Error(t, err)
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "want a JSON-RPC error, got %T", err)
	return rpcErr
}

func errorReason(t *testing.T, rpcErr *client.RPCError) string {
	t.Helper()
	raw, err := json.Marshal(rpcErr.Data)
	require.NoError(t, err)
	var data agents.ErrorData
	require.NoError(t, json.Unmarshal(raw, &data), "error must carry a data object")
	return data.Reason
}

func TestExtensionAdvertised(t *testing.T) {
	tc, _ := newAgentsServer(t, sepWorkflowAgent())
	assert.True(t, tc.ServerSupportsExtension(agents.ExtensionID),
		"server must advertise %q in capabilities.extensions", agents.ExtensionID)

	cap, ok := tc.ServerExtensionCapability(agents.ExtensionID)
	require.True(t, ok)
	assert.Equal(t, core.ExtensionCapability{"listChanged": true}, cap,
		"Register promises list_changed, since AddAgent and RemoveAgent always broadcast")
}

// agents/list carries routing cards only. If instructions or schemas leak
// into it, a supervisor host eager-loads every agent's tools at connect, which
// is the cost the primitive exists to avoid.
func TestListReturnsCardsWithoutDetail(t *testing.T) {
	tc, _ := newAgentsServer(t, sepWorkflowAgent())

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListAgentsResult
	require.NoError(t, res.Unmarshal(&out))

	assert.Equal(t, core.ResultTypeComplete, out.ResultType)
	require.Len(t, out.Agents, 1)
	assert.Equal(t, agents.AgentCard{
		Name:         "workflow-agent",
		Description:  "Investigates delivery pipelines and approvals",
		Capabilities: []string{"pipeline failures", "deployment readiness", "approval status"},
	}, out.Agents[0])
	assert.NotContains(t, string(res.Raw), "instructions")
	assert.NotContains(t, string(res.Raw), "list_failed_pipelines")
}

func TestGetReturnsDetail(t *testing.T) {
	tc, _ := newAgentsServer(t, sepWorkflowAgent())

	res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "workflow-agent"})
	require.NoError(t, err)
	var out agents.GetAgentResult
	require.NoError(t, res.Unmarshal(&out))

	assert.Equal(t, core.ResultTypeComplete, out.ResultType)
	assert.Equal(t, "workflow-agent", out.Agent)
	assert.Equal(t, "Handle delivery readiness and approval workflows.", out.Instructions)
	assert.Equal(t, &agents.ModelRoutingHint{Role: agents.ModelRoleMedium}, out.ModelRoutingHint)
	require.Len(t, out.Tools, 1)
	assert.Equal(t, "list_failed_pipelines", out.Tools[0].Name)
}

// toolsByName returns the caller's tools/list keyed by name.
func toolsByName(t *testing.T, call func(method string, params any) (*client.CallResult, error)) map[string]json.RawMessage {
	t.Helper()
	res, err := call("tools/list", nil)
	require.NoError(t, err)
	var out struct {
		Tools []json.RawMessage `json:"tools"`
	}
	require.NoError(t, res.Unmarshal(&out))
	byName := map[string]json.RawMessage{}
	for _, raw := range out.Tools {
		var head struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &head))
		byName[head.Name] = raw
	}
	return byName
}

// assertToolsMatchList is the SEP's "all fields MUST match" rule: every tool
// agents/get returns appears in the same caller's tools/list with identical
// JSON.
func assertToolsMatchList(t *testing.T, call func(method string, params any) (*client.CallResult, error), agent string) {
	t.Helper()
	listed := toolsByName(t, call)

	res, err := call(agents.MethodGet, agents.GetAgentParams{Name: agent})
	require.NoError(t, err)
	var out struct {
		Tools []json.RawMessage `json:"tools"`
	}
	require.NoError(t, res.Unmarshal(&out))
	require.NotEmpty(t, out.Tools)

	for _, raw := range out.Tools {
		var head struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(raw, &head))
		want, ok := listed[head.Name]
		require.True(t, ok, "agents/get returned %q, which this caller's tools/list does not hold", head.Name)
		assert.JSONEq(t, string(want), string(raw), "tool %q differs between agents/get and tools/list", head.Name)
	}
}

func TestGetToolsMatchToolsList(t *testing.T) {
	t.Run("client without fileInputs", func(t *testing.T) {
		tc, _ := newAgentsServer(t, opsAgent())
		assertToolsMatchList(t, testCall(t, tc.Client), "ops-agent")
	})
	t.Run("client with fileInputs", func(t *testing.T) {
		srv := newCatalogServer()
		_, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{opsAgent()}})
		require.NoError(t, err)
		tc := testutil.NewTestClient(t, srv, client.WithFileInputs())
		assertToolsMatchList(t, testCall(t, tc.Client), "ops-agent")
	})
}

// A tool re-registered after the agent was declared must show up in its new
// form. A copied definition would keep serving the old one.
func TestGetToolsFollowReregistration(t *testing.T) {
	srv := newCatalogServer()
	_, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{sepWorkflowAgent()}})
	require.NoError(t, err)
	srv.RegisterTool(core.ToolDef{Name: "list_failed_pipelines", Description: "v2", InputSchema: map[string]any{"type": "object"}}, noopTool)
	tc := testutil.NewTestClient(t, srv)

	res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "workflow-agent"})
	require.NoError(t, err)
	var out agents.GetAgentResult
	require.NoError(t, res.Unmarshal(&out))
	require.Len(t, out.Tools, 1)
	assert.Equal(t, "v2", out.Tools[0].Description)
}

func TestGetUnknownAgentIsNotFound(t *testing.T) {
	tc, _ := newAgentsServer(t, sepWorkflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "no-such-agent"})
	rpcErr := rpcError(t, err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpcErr.Code)
	assert.Equal(t, agents.ReasonAgentNotFound, errorReason(t, rpcErr))
}

func TestGetEmptyNameIsInvalidParams(t *testing.T) {
	tc, _ := newAgentsServer(t, sepWorkflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{})
	assert.Equal(t, core.ErrCodeInvalidParams, rpcError(t, err).Code)
}

// A caller must not be able to tell a hidden agent from a nonexistent one:
// it is missing from the roster, and agents/get answers with the identical
// error object.
func TestHiddenAgentIsIndistinguishableFromMissing(t *testing.T) {
	hide := func(_ core.MethodContext, name string) bool { return name != "ops-agent" }
	tc, _ := newAgentsServerWith(t, agents.Config{Visible: hide}, sepWorkflowAgent(), opsAgent())

	out := listAll(t, testCall(t, tc.Client))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "workflow-agent", out.Agents[0].Name)

	_, hiddenErr := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "ops-agent"})
	_, missingErr := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "no-such-agent"})
	hidden, missing := rpcError(t, hiddenErr), rpcError(t, missingErr)
	assert.Equal(t, missing.Code, hidden.Code)
	assert.Equal(t, missing.Message, hidden.Message)
	assert.Equal(t, missing.Data, hidden.Data)
}

func TestUnregisteredToolIsInvalidDefinition(t *testing.T) {
	broken := agents.AgentDef{Name: "broken", Tools: []string{"approve_gate", "no_such_tool"}}

	srv := newCatalogServer()
	reg, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{broken}})
	require.Error(t, err, "Register must report a tool name the catalog does not hold")
	assert.Contains(t, err.Error(), "no_such_tool")
	require.NotNil(t, reg)
	tc := testutil.NewTestClient(t, srv)

	_, err = tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "broken"})
	rpcErr := rpcError(t, err)
	assert.Equal(t, core.ErrCodeInternal, rpcErr.Code)
	assert.Equal(t, agents.ReasonInvalidAgentDefinition, errorReason(t, rpcErr))

	require.Error(t, reg.AddAgent(agents.AgentDef{Name: "also-broken", Tools: []string{"nope"}}),
		"AddAgent reports unregistered tools the same way")
}

func TestCacheHints(t *testing.T) {
	t.Run("private and immediately stale by default", func(t *testing.T) {
		tc, _ := newAgentsServer(t, sepWorkflowAgent())
		out := listAll(t, testCall(t, tc.Client))
		assert.Equal(t, core.CacheScopePrivate, out.CacheScope)
		require.NotNil(t, out.TTLMs)
		assert.Equal(t, 0, *out.TTLMs)
	})
	t.Run("configured", func(t *testing.T) {
		ttl := 5000
		tc, _ := newAgentsServerWith(t, agents.Config{TTLMs: &ttl, CacheScope: core.CacheScopePublic}, sepWorkflowAgent())
		out := listAll(t, testCall(t, tc.Client))
		assert.Equal(t, core.CacheScopePublic, out.CacheScope)
		require.NotNil(t, out.TTLMs)
		assert.Equal(t, 5000, *out.TTLMs)

		res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "workflow-agent"})
		require.NoError(t, err)
		var get agents.GetAgentResult
		require.NoError(t, res.Unmarshal(&get))
		assert.Equal(t, core.CacheScopePublic, get.CacheScope)
		require.NotNil(t, get.TTLMs)
		assert.Equal(t, 5000, *get.TTLMs)
	})
}

func TestListPagination(t *testing.T) {
	tc, _ := newAgentsServerWith(t, agents.Config{PageSize: 2},
		agents.AgentDef{Name: "alpha"}, agents.AgentDef{Name: "bravo"}, agents.AgentDef{Name: "charlie"})

	first := listAll(t, testCall(t, tc.Client))
	require.Len(t, first.Agents, 2)
	require.NotEmpty(t, first.NextCursor)

	res, err := tc.Client.Call(t.Context(), agents.MethodList, agents.ListAgentsParams{Cursor: first.NextCursor})
	require.NoError(t, err)
	var second agents.ListAgentsResult
	require.NoError(t, res.Unmarshal(&second))
	require.Len(t, second.Agents, 1)
	assert.Equal(t, "charlie", second.Agents[0].Name)
	assert.Empty(t, second.NextCursor, "the last page omits nextCursor")

	_, err = tc.Client.Call(t.Context(), agents.MethodList, agents.ListAgentsParams{Cursor: "not-a-cursor"})
	assert.Equal(t, core.ErrCodeInvalidParams, rpcError(t, err).Code)
}

func TestListUnpaginatedByDefault(t *testing.T) {
	tc, _ := newAgentsServer(t, agents.AgentDef{Name: "alpha"}, agents.AgentDef{Name: "bravo"}, agents.AgentDef{Name: "charlie"})
	out := listAll(t, testCall(t, tc.Client))
	assert.Len(t, out.Agents, 3)
	assert.Empty(t, out.NextCursor)
}

// A duplicate name in the initial roster is reported, and the first
// registration wins rather than being silently overwritten.
func TestRegisterDuplicateReportsError(t *testing.T) {
	srv := newCatalogServer()
	dup := sepWorkflowAgent()
	dup.Description = "SHOULD NOT WIN"
	reg, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{sepWorkflowAgent(), dup}})
	require.Error(t, err, "duplicate name must be reported")
	require.NotNil(t, reg)

	out := listAll(t, testCall(t, testutil.NewTestClient(t, srv).Client))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "Investigates delivery pipelines and approvals", out.Agents[0].Description)
}

func TestListOrderIsStable(t *testing.T) {
	tc, reg := newAgentsServer(t, agents.AgentDef{Name: "alpha"}, agents.AgentDef{Name: "bravo"})
	require.NoError(t, reg.AddAgent(agents.AgentDef{Name: "charlie"}))

	out := listAll(t, testCall(t, tc.Client))
	names := []string{out.Agents[0].Name, out.Agents[1].Name, out.Agents[2].Name}
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, names)
}

func TestAddRemoveAgent(t *testing.T) {
	tc, reg := newAgentsServer(t, sepWorkflowAgent())

	require.Error(t, reg.AddAgent(sepWorkflowAgent()), "AddAgent must reject a duplicate")

	require.NoError(t, reg.AddAgent(agents.AgentDef{Name: "second"}))
	reg.RemoveAgent("workflow-agent")
	reg.RemoveAgent("nonexistent")

	out := listAll(t, testCall(t, tc.Client))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "second", out.Agents[0].Name)
}

// testCall adapts a client to the call signature the shared helpers take, so
// the same assertions run against the session and stateless wires.
func testCall(t *testing.T, c *client.Client) func(method string, params any) (*client.CallResult, error) {
	return func(method string, params any) (*client.CallResult, error) {
		return c.Call(t.Context(), method, params)
	}
}

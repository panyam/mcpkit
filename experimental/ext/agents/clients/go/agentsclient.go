package agentsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
)

// Client wraps a *client.Client with the agents discovery workflow:
// capability detection, roster fetch (agents/list), and detail resolution
// (agents/get). It holds no state of its own; methods are safe for concurrent
// use to the same degree the underlying *client.Client is.
type Client struct {
	mcp *client.Client
}

// New builds an agents host helper over the given mcpkit client. The
// underlying client must already be Connect()ed for the discovery methods to
// succeed.
func New(mcp *client.Client) *Client {
	return &Client{mcp: mcp}
}

// SupportsAgents reports whether the connected server advertised the agents
// extension in its initialize (or server/discover) response. Reads the cached
// handshake, so it makes no network call. Hosts iterating connected servers
// use this to skip discovery against servers that do not speak the primitive.
func (c *Client) SupportsAgents() bool {
	return c.mcp.ServerSupportsExtension(agents.ExtensionID)
}

// ListAgentsPage calls agents/list once and returns the whole result,
// including NextCursor and the SEP-2549 cache hints. Pass an empty cursor for
// the first page. Use it when the host caches the roster or pages lazily.
//
// A result with no "agents" key decodes to an empty, non-nil slice.
// ctx controls cancellation of the underlying POST on Streamable HTTP.
func (c *Client) ListAgentsPage(ctx context.Context, cursor string) (agents.ListAgentsResult, error) {
	var params any
	if cursor != "" {
		params = agents.ListAgentsParams{Cursor: cursor}
	}
	res, err := c.mcp.CallContext(ctx, client.NewCallContext(ctx), agents.MethodList, params)
	if err != nil {
		return agents.ListAgentsResult{}, fmt.Errorf("agents/list: %w", err)
	}
	var out agents.ListAgentsResult
	if err := res.Unmarshal(&out); err != nil {
		return agents.ListAgentsResult{}, fmt.Errorf("agents/list: decode: %w", err)
	}
	if out.Agents == nil {
		out.Agents = []agents.AgentCard{}
	}
	return out, nil
}

// ListAgents walks every agents/list page and returns the full visible
// roster: routing cards only, no tool schemas. An empty roster is an empty
// slice, not an error.
//
// A server that keeps returning a cursor it has already returned would loop
// forever, so a repeated cursor is reported as an error.
func (c *Client) ListAgents(ctx context.Context) ([]agents.AgentCard, error) {
	var all []agents.AgentCard
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := c.ListAgentsPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Agents...)
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("agents/list: server repeated cursor %q", page.NextCursor)
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	if all == nil {
		all = []agents.AgentCard{}
	}
	return all, nil
}

// GetAgent calls agents/get for one agent name and returns its details:
// instructions, the optional model routing hint, and the scoped tool
// definitions, which match the same caller's tools/list entries.
//
// Server errors come back wrapped around a *client.RPCError. Test them with
// IsAgentNotFound and IsInvalidAgentDefinition.
// ctx controls cancellation of the underlying POST on Streamable HTTP.
func (c *Client) GetAgent(ctx context.Context, name string) (agents.GetAgentResult, error) {
	res, err := c.mcp.CallContext(ctx, client.NewCallContext(ctx), agents.MethodGet, agents.GetAgentParams{Name: name})
	if err != nil {
		return agents.GetAgentResult{}, fmt.Errorf("agents/get %q: %w", name, err)
	}
	var out agents.GetAgentResult
	if err := res.Unmarshal(&out); err != nil {
		return agents.GetAgentResult{}, fmt.Errorf("agents/get %q: decode: %w", name, err)
	}
	return out, nil
}

// IsAgentNotFound reports whether err is the server saying the agent does not
// exist or is hidden from this caller (-32602, reason agent_not_found). The
// two are indistinguishable by design. A host may refresh the roster and
// retry.
func IsAgentNotFound(err error) bool {
	return hasReason(err, core.ErrCodeInvalidParams, agents.ReasonAgentNotFound)
}

// IsInvalidAgentDefinition reports whether err is the server saying the agent
// is misconfigured (-32603, reason invalid_agent_definition). A host should
// not retry the same request immediately; it may retry after its cached
// discovery state expires or the roster changes.
func IsInvalidAgentDefinition(err error) bool {
	return hasReason(err, core.ErrCodeInternal, agents.ReasonInvalidAgentDefinition)
}

// StepUpScopes reports whether err is agents/get refusing an agent the caller
// may see but not open (an AgentDef.ScopeChallenge on the server), and returns
// the scopes the challenge asks for. The server answers with an HTTP 403
// carrying WWW-Authenticate error="insufficient_scope", which the client
// surfaces as *client.ClientAuthError. A host that can obtain a token with
// those scopes, for example the union of them with what it already holds, may
// retry with it. ok is true with no scopes when the server refused without
// naming a remedy. A Cache never stores the refusal, so the retry reaches the
// server.
func StepUpScopes(err error) (scopes []string, ok bool) {
	var ae *client.ClientAuthError
	if !errors.As(err, &ae) || ae.StatusCode != http.StatusForbidden {
		return nil, false
	}
	return ae.RequiredScopes, true
}

func hasReason(err error, code int, reason string) bool {
	var rpcErr *client.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != code || rpcErr.Data == nil {
		return false
	}
	raw, mErr := json.Marshal(rpcErr.Data)
	if mErr != nil {
		return false
	}
	var data agents.ErrorData
	if json.Unmarshal(raw, &data) != nil {
		return false
	}
	return data.Reason == reason
}

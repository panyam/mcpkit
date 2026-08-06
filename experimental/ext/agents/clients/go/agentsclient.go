package agentsclient

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/client"
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

// SupportsAgents reports whether the connected server advertised the
// io.modelcontextprotocol/agents extension in its initialize (or
// server/discover) response. Reads the cached handshake — no network call.
// Hosts iterating connected servers use this to skip ListAgents against
// servers that do not speak the primitive.
func (c *Client) SupportsAgents() bool {
	return c.mcp.ServerSupportsExtension(agents.ExtensionID)
}

// ListAgents calls agents/list and returns the roster of summaries (routing
// fields only, no tool schemas). This is the level-2 read: cheap, sent to
// every host, and enough to route on.
//
// Tolerant decode: a result with no "agents" key, or a null/absent list,
// decodes to an empty slice with no error, so a host can treat "no agents" and
// "empty roster" the same. Transport and JSON-shape errors propagate.
//
// ctx controls cancellation of the underlying POST on Streamable HTTP.
func (c *Client) ListAgents(ctx context.Context) ([]agents.AgentSummary, error) {
	res, err := c.mcp.CallContext(ctx, client.NewCallContext(ctx), agents.MethodList, nil)
	if err != nil {
		return nil, fmt.Errorf("agents/list: %w", err)
	}
	var out agents.ListResult
	if err := res.Unmarshal(&out); err != nil {
		return nil, fmt.Errorf("agents/list: decode: %w", err)
	}
	return out.Agents, nil
}

// GetAgent calls agents/get for one agentId and returns the resolved detail
// (summary fields + instructions + scoped tool schemas). This is the level-3
// read a host issues only after choosing an agent from the roster.
//
// An unknown or empty agentId surfaces as the server's JSON-RPC error (mapped
// to *client.RPCError with code InvalidParams). Transport and JSON-shape
// errors propagate.
//
// ctx controls cancellation of the underlying POST on Streamable HTTP.
func (c *Client) GetAgent(ctx context.Context, agentID string) (agents.AgentDetail, error) {
	res, err := c.mcp.CallContext(ctx, client.NewCallContext(ctx), agents.MethodGet, agents.GetParams{AgentID: agentID})
	if err != nil {
		return agents.AgentDetail{}, fmt.Errorf("agents/get %q: %w", agentID, err)
	}
	var out agents.GetResult
	if err := res.Unmarshal(&out); err != nil {
		return agents.AgentDetail{}, fmt.Errorf("agents/get %q: decode: %w", agentID, err)
	}
	return out.Agent, nil
}

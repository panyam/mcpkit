package main

import (
	"context"
	"errors"
	"sync"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
)

// swapToken is the caller's bearer token. A host that steps up swaps in the
// broader token and keeps the same connection, since both tokens belong to
// the same user.
type swapToken struct {
	mu  sync.Mutex
	tok string
}

func (s *swapToken) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok, nil
}

func (s *swapToken) set(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = tok
}

// caller is one connected host identity: an MCP client, the agents helper
// over it, and a cache that follows the roster.
type caller struct {
	mcp    *client.Client
	agents *agentsclient.Client
	cache  *agentsclient.Cache
	token  *swapToken

	mu       sync.Mutex
	onChange func()
}

// connect opens a session as the holder of token. The notification callback
// is installed before Connect because the session wire delivers
// notifications/agents/list_changed on its GET stream, and the cache it
// invalidates is only known afterwards.
func connect(ctx context.Context, url, token string, opts ...client.ClientOption) (*caller, error) {
	k := &caller{token: &swapToken{tok: token}}
	opts = append([]client.ClientOption{
		client.WithTokenSource(k.token),
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, _ any) {
			if method == agents.NotificationListChanged {
				k.changed()
			}
		}),
	}, opts...)
	k.mcp = client.NewClient(url, core.ClientInfo{Name: "agents-discovery-host", Version: "1.0"}, opts...)
	if err := k.mcp.Connect(ctx); err != nil {
		return nil, err
	}
	k.agents = agentsclient.New(k.mcp)
	k.cache = agentsclient.NewCache(k.agents)
	return k, nil
}

func (k *caller) changed() {
	k.cache.Invalidate()
	k.mu.Lock()
	fn := k.onChange
	k.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// watch arranges for fn to run on every list_changed. On the stateless wire
// that takes a subscriptions/listen stream, which WatchListChanged opens. On
// the session wire the callback installed by connect already receives it.
func (k *caller) watch(ctx context.Context, fn func()) (stop func(), err error) {
	k.mu.Lock()
	k.onChange = fn
	k.mu.Unlock()
	l, err := agentsclient.WatchListChanged(ctx, k.mcp, k.changed)
	if errors.Is(err, client.ErrListenNeedsStatelessWire) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	return l.Stop, nil
}

func (k *caller) close() { k.mcp.Close() }

// rosterNames lists the agents this caller can see, through the cache.
func (k *caller) rosterNames(ctx context.Context) ([]string, error) {
	cards, err := k.cache.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out, nil
}

// models is the host's own mapping from routing-hint roles to model names.
var models = agentsclient.RoleTable[string]{
	Roles: map[agents.ModelRole]agentsclient.RoleModel[string]{
		agents.ModelRoleSmall: {Model: "claude-haiku-5-5"},
		agents.ModelRoleLarge: {Model: "claude-opus-5-5", Vision: true},
	},
	Default: "claude-sonnet-5-5",
}

// runAgent stands in for the host's local subagent loop. A real host would
// hand the instructions and tool schemas to a model and let it choose calls.
// Here the "model" calls each scoped tool once, through tools/call, which is
// the only execution path the extension uses.
func (k *caller) runAgent(ctx context.Context, detail agents.GetAgentResult) (map[string]string, error) {
	out := map[string]string{}
	for _, td := range detail.Tools {
		args := map[string]any{}
		switch td.Name {
		case "get_pipeline_log":
			args["id"] = "build-1841"
		case "deploy_service":
			args["service"] = "checkout"
		}
		text, err := k.mcp.ToolCall(ctx, td.Name, args)
		if err != nil {
			return nil, err
		}
		out[td.Name] = text
	}
	return out, nil
}

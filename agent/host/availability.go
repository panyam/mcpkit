package host

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// availabilitySource wraps a server's ToolSource so that a call which fails
// because the server is unreachable right now — a transport error the client
// could not heal by reconnecting — surfaces as agent.ErrNotAvailableNow with a
// model-friendly message instead of a raw "connection refused". The Runner
// turns ErrNotAvailableNow into a non-fatal EventToolUnavailable and the turn
// continues (the model can retry, route around it, or tell the user).
//
// Tools() is delegated unchanged: the defs stay listed even while the server is
// down (keep-the-defs, docs/AGENT_SERVER_STATE.md), so the model is not
// surprised by a shrinking tool set mid-turn. This is the reactive half of
// graceful degradation — a proactive health poll (marking a server down before
// a call hits it) can be added later without changing this.
type availabilitySource struct {
	inner    agent.ToolSource
	serverID string
}

func newAvailabilitySource(inner agent.ToolSource, serverID string) *availabilitySource {
	return &availabilitySource{inner: inner, serverID: serverID}
}

func (s *availabilitySource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return s.inner.Tools(ctx)
}

func (s *availabilitySource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	res, err := s.inner.Call(ctx, name, args)
	// A transient (network-level) error means the server is unreachable: the
	// client already tried to reconnect and could not. A tool that ran and
	// failed reports via ToolResult.IsError (no error here), and a server-side
	// JSON-RPC / 4xx error is not transient — both fall through unchanged.
	if err != nil && client.IsTransientError(err) {
		return nil, fmt.Errorf("%w: tool %q is temporarily unavailable — server %q is not reachable right now (%v); retry, use another approach, or tell the user",
			agent.ErrNotAvailableNow, name, s.serverID, err)
	}
	return res, err
}

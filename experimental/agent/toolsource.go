package agent

import (
	"context"
	"errors"

	"github.com/panyam/mcpkit/core"
)

// ErrUnknownTool is wrapped by ToolSource implementations when a Call names
// a tool the source does not have. Aggregators use it to distinguish a
// stale-index miss (worth one refresh) from a definitive dispatch failure;
// the Runner uses it to feed a not-found back to the model.
var ErrUnknownTool = errors.New("agent: unknown tool")

// ToolSource is the Runner's view of callable tools, whatever their origin:
// a connected MCP server, a host-local function, or an aggregation of other
// sources. Implementations must be safe for concurrent use; the Runner may
// dispatch parallel tool calls against one source.
type ToolSource interface {
	// Tools returns the definitions the model should see. The returned
	// slice is a snapshot; implementations decide their own caching and
	// refresh policy. Every returned name must be callable via Call, and
	// names within one source must be unique (aggregators treat a
	// repeated name from the same source as one claim, first definition
	// wins; only cross-source collisions get disambiguation).
	Tools(ctx context.Context) ([]core.ToolDef, error)

	// Call dispatches one tool invocation by name. A non-nil error means
	// the dispatch itself failed (unknown tool, transport failure,
	// unresolved ambiguity); a tool that ran and failed reports through
	// ToolResult.IsError instead, so the Runner can feed the failure back
	// to the model rather than aborting the turn.
	Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error)
}

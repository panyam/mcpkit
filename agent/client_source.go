package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// ErrInputRequired is returned by ClientSource.Call when the server asks for
// SEP-2322 mid-call input and no InputHandler was configured. The elicitation
// seam (agent epic milestone 2) replaces the default handler; until then,
// callers can detect the condition with errors.Is.
var ErrInputRequired = errors.New("agent: tool requires mid-call input and no InputHandler is configured")

// ErrTaskResult is returned by ClientSource.Call when the server elects to
// run the tool as a SEP-2663 task. Task-aware dispatch (polling, resume) is
// a later milestone; until then the Runner surfaces this as a dispatch
// failure rather than silently dropping the task.
var ErrTaskResult = errors.New("agent: tool returned a task; task-aware dispatch is not configured")

// ClientSource adapts one connected client.Client into a ToolSource.
//
// Calls go through the client's MRTR-aware path so ctx cancellation reaches
// the wire and mid-call input requests surface deterministically: with the
// default configuration an input-required round fails with ErrInputRequired
// instead of hanging or guessing.
type ClientSource struct {
	c       *client.Client
	handler client.InputHandler
}

// ClientSourceOption configures a ClientSource.
type ClientSourceOption func(*ClientSource)

// WithInputHandler installs the SEP-2322 input handler used when a tool call
// returns input_required mid-dispatch. The elicitation seam wires the host UI
// through this option; tests can use it to script input rounds.
func WithInputHandler(h client.InputHandler) ClientSourceOption {
	return func(s *ClientSource) { s.handler = h }
}

// NewClientSource wraps a connected client. The client must already be
// initialized; ClientSource performs no connection management.
func NewClientSource(c *client.Client, opts ...ClientSourceOption) *ClientSource {
	s := &ClientSource{
		c: c,
		handler: func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
			return nil, ErrInputRequired
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Tools lists the server's tools in model-facing form (server-side
// visibility filtering applied, per ListToolsForModel).
func (s *ClientSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return s.c.ListToolsForModel(ctx)
}

// Call dispatches tools/call with automatic MRTR rounds via the configured
// InputHandler. A task-creating response maps to ErrTaskResult until
// task-aware dispatch lands.
func (s *ClientSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	res, err := client.CallToolWithInputs(ctx, s.c, name, args, s.handler)
	if err != nil {
		return nil, err
	}
	switch {
	case res.Sync != nil:
		return res.Sync, nil
	case res.Task != nil:
		return nil, fmt.Errorf("%w (task %s)", ErrTaskResult, res.Task.TaskID)
	default:
		return nil, fmt.Errorf("agent: unexpected tool call result shape for %q", name)
	}
}

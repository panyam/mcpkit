package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// ErrInputRequired is returned by ClientSource.Call when the server asks for
// SEP-2322 mid-call input and no InputHandler was configured. The elicitation
// seam (agent epic milestone 2) replaces the default handler; until then,
// callers can detect the condition with errors.Is.
var ErrInputRequired = errors.New("agent: tool requires mid-call input and no InputHandler is configured")

// ClientSource adapts one connected client.Client into a ToolSource.
//
// Calls go through the client's MRTR-aware path so ctx cancellation reaches
// the wire and mid-call input requests surface deterministically: with the
// default configuration an input-required round fails with ErrInputRequired
// instead of hanging or guessing.
type ClientSource struct {
	c            *client.Client
	handler      client.InputHandler
	onTaskStatus func(*core.DetailedTask)

	taskGrace  time.Duration
	onDetach   func(*BackgroundTask)
	onComplete func(*BackgroundTask)
}

// ClientSourceOption configures a ClientSource.
type ClientSourceOption func(*ClientSource)

// WithInputHandler installs the SEP-2322 input handler used when a tool call
// returns input_required mid-dispatch. The elicitation seam wires the host UI
// through this option; tests can use it to script input rounds.
func WithInputHandler(h client.InputHandler) ClientSourceOption {
	return func(s *ClientSource) { s.handler = h }
}

// WithTaskStatusHook observes every polled task snapshot during task-backed
// tool calls: the client-level WaitOptions.OnStatus threaded through, so
// surfaces can render progress between tool-begin and tool-end.
func WithTaskStatusHook(fn func(*core.DetailedTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onTaskStatus = fn }
}

// WithTaskGrace opts task-backed calls into background detach: a call still
// working when the window expires returns immediately with a model-visible
// "running in the background" result while polling continues on a detached
// goroutine. The window holds while an input pause is active, so
// interactive tasks that park in input_required within the grace stay
// inline. Zero or unset keeps the synchronous wait-to-terminal contract.
func WithTaskGrace(d time.Duration) ClientSourceOption {
	return func(s *ClientSource) { s.taskGrace = d }
}

// WithTaskDetachHook observes each detach, delivering the BackgroundTask
// handle (registries, transcript lines, cancellation surfaces hang off it).
func WithTaskDetachHook(fn func(*BackgroundTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onDetach = fn }
}

// WithTaskCompletionHook fires when a DETACHED task reaches its terminal
// state (inline completions return through Call as usual). Runs on the
// background poll goroutine; hosts turning completions into events or
// proactive turns do it here.
func WithTaskCompletionHook(fn func(*BackgroundTask)) ClientSourceOption {
	return func(s *ClientSource) { s.onComplete = fn }
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
// InputHandler. A task-creating response is waited to a terminal state:
// polling honors the server's interval hints, input_required pauses resolve
// through the SAME InputHandler (so task input reaches the elicitation seam
// with no extra wiring), and the terminal snapshot maps back onto the sync
// contract (completed carries the ToolResult, including tool-side IsError;
// failed and cancelled are dispatch errors).
func (s *ClientSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	res, err := client.CallToolWithInputs(ctx, s.c, name, args, s.handler)
	if err != nil {
		return nil, err
	}
	switch {
	case res.Sync != nil:
		return res.Sync, nil
	case res.Task != nil:
		return s.waitTask(ctx, name, res.Task.TaskID)
	default:
		return nil, fmt.Errorf("agent: unexpected tool call result shape for %q", name)
	}
}

func (s *ClientSource) waitTask(ctx context.Context, name, taskID string) (*core.ToolResult, error) {
	if s.taskGrace > 0 {
		return s.waitTaskWithGrace(ctx, name, taskID)
	}
	dt, err := client.WaitForTaskWithInput(ctx, s.c, taskID, s.handler,
		client.WaitOptions{OnStatus: s.onTaskStatus})
	if err != nil {
		return nil, err
	}
	return mapTerminalTask(dt, name, taskID)
}

// waitTaskWithGrace races the task against the grace window. The poll runs
// on its own goroutine with a detached context from the start, so a detach
// is a handoff, not a restart; an inline finish simply consumes the same
// outcome before the timer wins.
func (s *ClientSource) waitTaskWithGrace(ctx context.Context, name, taskID string) (*core.ToolResult, error) {
	pollCtx, cancelPoll := context.WithCancel(context.WithoutCancel(ctx))
	bt := &BackgroundTask{
		TaskID: taskID, Tool: name, StartedAt: time.Now(),
		c: s.c, cancelPoll: cancelPoll, done: make(chan struct{}),
	}

	var detached atomic.Bool
	onStatus := func(dt *core.DetailedTask) {
		bt.setStatus(dt.Status)
		if s.onTaskStatus != nil {
			s.onTaskStatus(dt)
		}
	}
	go func() {
		dt, err := client.WaitForTaskWithInput(pollCtx, s.c, taskID, s.handler,
			client.WaitOptions{OnStatus: onStatus})
		if err != nil {
			bt.finish(nil, err)
		} else {
			res, mapErr := mapTerminalTask(dt, name, taskID)
			bt.finish(res, mapErr)
		}
		if detached.Load() && s.onComplete != nil {
			s.onComplete(bt)
		}
	}()

	timer := time.NewTimer(s.taskGrace)
	defer timer.Stop()
	for {
		select {
		case <-bt.done:
			return bt.Result()
		case <-ctx.Done():
			// Turn cancelled while still inline: abort the poll too.
			cancelPoll()
			<-bt.done
			return nil, ctx.Err()
		case <-timer.C:
			if bt.Status() == core.TaskInputRequired {
				// An active input pause is an interactive moment; hold
				// the grace and re-check after another window.
				timer.Reset(s.taskGrace)
				continue
			}
			detached.Store(true)
			if s.onDetach != nil {
				s.onDetach(bt)
			}
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "task " + taskID + " (" + name +
				") is still running and has moved to the background; the user can keep working and will be told when it finishes."}}}, nil
		}
	}
}

func mapTerminalTask(dt *core.DetailedTask, name, taskID string) (*core.ToolResult, error) {
	switch dt.Status {
	case core.TaskCompleted:
		if dt.Result == nil {
			return nil, fmt.Errorf("agent: task %s (%s) completed without a result", taskID, name)
		}
		return dt.Result, nil
	case core.TaskFailed:
		msg := "unknown error"
		if dt.Error != nil {
			msg = dt.Error.Message
		}
		return nil, fmt.Errorf("agent: task %s (%s) failed: %s", taskID, name, msg)
	case core.TaskCancelled:
		return nil, fmt.Errorf("agent: task %s (%s) was cancelled", taskID, name)
	default:
		return nil, fmt.Errorf("agent: task %s (%s) ended in unexpected status %q", taskID, name, dt.Status)
	}
}

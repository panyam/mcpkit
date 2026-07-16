package agent

import (
	"context"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// DefaultTaskGrace is the recommended grace window for hosts that opt into
// background detach via WithTaskGrace. Detaching is opt-in: a ClientSource
// without WithTaskGrace waits inline to a terminal state (the synchronous
// contract task dispatch shipped with).
const DefaultTaskGrace = 10 * time.Second

// BackgroundTask is the handle for a task that outlived its grace window.
// The poll loop keeps running (same InputHandler, so later input_required
// pauses still reach the host's elicitation seam, asynchronously), and the
// terminal outcome is delivered through the completion hook and retained on
// the handle.
type BackgroundTask struct {
	// TaskID and Tool identify the task for surfaces (/tasks listings).
	TaskID string
	Tool   string
	// StartedAt is when the original tool call began.
	StartedAt time.Time

	c          *client.Client
	cancelPoll context.CancelFunc

	mu     sync.Mutex
	status core.TaskStatus
	result *core.ToolResult
	err    error

	done chan struct{}
}

// Status returns the most recent polled status.
func (bt *BackgroundTask) Status() core.TaskStatus {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return bt.status
}

// Done closes when the task reaches a terminal state or the poll aborts.
func (bt *BackgroundTask) Done() <-chan struct{} { return bt.done }

// Result returns the terminal outcome; valid after Done closes. The error
// covers failed/cancelled tasks and poll aborts; a completed task's
// tool-side failure rides ToolResult.IsError as usual.
func (bt *BackgroundTask) Result() (*core.ToolResult, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return bt.result, bt.err
}

// Cancel requests server-side cancellation (tasks/cancel) and stops the
// poll loop. The completion hook still fires (with the cancelled outcome),
// so surfaces observe one lifecycle regardless of how it ended.
func (bt *BackgroundTask) Cancel() error {
	err := client.CancelTask(bt.c, bt.TaskID)
	bt.cancelPoll()
	return err
}

func (bt *BackgroundTask) setStatus(s core.TaskStatus) {
	bt.mu.Lock()
	bt.status = s
	bt.mu.Unlock()
}

func (bt *BackgroundTask) finish(res *core.ToolResult, err error) {
	bt.mu.Lock()
	bt.result, bt.err = res, err
	bt.mu.Unlock()
	close(bt.done)
}

package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	. "github.com/panyam/mcpkit/server"
)

// newStatusMessageServer registers "stepper", a task tool that sets a status
// message, parks until the test releases it (or the task is cancelled), then
// tries to update the task again. The errors from those late updates are
// reported on lateErrs so the cancel test can assert on them.
func newStatusMessageServer(t *testing.T) (srv *Server, release chan struct{}, lateErrs chan [2]error) {
	t.Helper()
	srv = NewServer(core.ServerInfo{Name: "tasks-status-test", Version: "0.0.1"})
	release = make(chan struct{})
	lateErrs = make(chan [2]error, 1)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "stepper",
			Description: "Reports progress through statusMessage",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			tc := tasks.GetTaskContext(ctx)
			if tc == nil {
				return core.GoAsyncResult{}, nil
			}
			if err := tc.SetStatusMessage("step 1/2"); err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			select {
			case <-release:
				if err := tc.SetStatusMessage("step 2/2"); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				return core.TextResult("done"), nil
			case <-ctx.Done():
				// The store update doesn't depend on ctx, so these reach the
				// terminal guard even though the task's context is done.
				lateErrs <- [2]error{
					tc.SetStatusMessage("step 2/2"),
					tc.SetStatus(core.TaskWorking),
				}
				return core.TextResult("stopped"), nil
			}
		},
	)
	tasks.Register(tasks.Config{Server: srv})
	return srv, release, lateErrs
}

// TestV2_SetStatusMessage verifies the message shows on tasks/get and in
// notifications/tasks, and that setting it leaves the status alone.
func TestV2_SetStatusMessage(t *testing.T) {
	srv, release, _ := newStatusMessageServer(t)

	notifs := make(chan core.DetailedTask, 16)
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "status-test", Version: "0.0.1"},
		client.WithGetSSEStream(),
		client.WithTasksExtension(),
		client.WithNotificationCallback(func(method string, params any) {
			if method != "notifications/tasks" {
				return
			}
			raw, _ := json.Marshal(params)
			var dt core.DetailedTask
			if json.Unmarshal(raw, &dt) == nil {
				select {
				case notifs <- dt:
				default:
				}
			}
		}),
	)
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	res, err := client.ToolCall(t.Context(), c, "stepper", map[string]any{})
	if err != nil || !res.IsTask() {
		t.Fatalf("tools/call: err=%v task=%v", err, res != nil && res.IsTask())
	}
	taskID := res.Task.TaskID

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	d := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.StatusMessage == "step 1/2"
	})
	if d.Status != core.TaskWorking {
		t.Errorf("status = %q, want working (SetStatusMessage must not change it)", d.Status)
	}

	// The same message must also have been pushed.
	deadline := time.After(3 * time.Second)
	for got := false; !got; {
		select {
		case n := <-notifs:
			got = n.TaskID == taskID && n.StatusMessage == "step 1/2"
		case <-deadline:
			t.Fatal("no notifications/tasks carried statusMessage \"step 1/2\"")
		}
	}

	close(release)
	final := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status.IsTerminal()
	})
	if final.Status != core.TaskCompleted {
		t.Errorf("final status = %q, want completed", final.Status)
	}
}

// TestV2_StatusUpdatesCannotReviveCancelledTask verifies that SetStatus and
// SetStatusMessage called after tasks/cancel return ErrTaskTerminal and leave
// the task cancelled. Before the guard, SetStatus(TaskWorking) moved it back
// to working.
func TestV2_StatusUpdatesCannotReviveCancelledTask(t *testing.T) {
	srv, _, lateErrs := newStatusMessageServer(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := client.ToolCall(t.Context(), c, "stepper", map[string]any{})
	if err != nil || !res.IsTask() {
		t.Fatalf("tools/call: err=%v", err)
	}
	taskID := res.Task.TaskID

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.StatusMessage == "step 1/2"
	})
	if err := client.CancelTask(ctx, c, taskID); err != nil {
		t.Fatalf("tasks/cancel: %v", err)
	}

	select {
	case errs := <-lateErrs:
		for i, name := range []string{"SetStatusMessage", "SetStatus"} {
			if !errors.Is(errs[i], tasks.ErrTaskTerminal) {
				t.Errorf("%s after cancel: err = %v, want ErrTaskTerminal", name, errs[i])
			}
		}
	case <-ctx.Done():
		t.Fatal("tool never observed the cancellation")
	}

	d, err := client.GetTask(ctx, c, taskID)
	if err != nil {
		t.Fatalf("tasks/get: %v", err)
	}
	if d.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", d.Status)
	}
	if d.StatusMessage == "step 2/2" {
		t.Error("statusMessage was overwritten after cancel")
	}
}

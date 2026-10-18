package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
)

// stallingStore holds the tasks/result handler's first blocking wait until
// the task reaches a target status, then a little longer, before letting it
// register. That forces the interleaving behind issue 390: the tool changes
// state after the handler last looked and before it starts waiting.
type stallingStore struct {
	TaskStore
	until  func(core.TaskStatus) bool
	parked chan struct{} // closed when the handler reaches its first wait
	once   sync.Once
}

func newStallingStore(until func(core.TaskStatus) bool) *stallingStore {
	return &stallingStore{TaskStore: NewInMemoryStore(), until: until, parked: make(chan struct{})}
}

func (s *stallingStore) stall(ctx context.Context, taskID, sessionID string) {
	s.once.Do(func() {
		close(s.parked)
		for ctx.Err() == nil {
			if info, ok := s.TaskStore.Get(taskID, sessionID); ok && s.until(info.Status) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		// Let the tool finish whatever follows its status change (the
		// side-channel send, or storing the result) before registering.
		time.Sleep(20 * time.Millisecond)
	})
}

func (s *stallingStore) WaitForUpdate(ctx context.Context, taskID, sessionID string) error {
	s.stall(ctx, taskID, sessionID)
	return s.TaskStore.WaitForUpdate(ctx, taskID, sessionID)
}

func (s *stallingStore) WaitForResult(ctx context.Context, taskID, sessionID string) (core.ToolResult, core.TaskInfo, error) {
	s.stall(ctx, taskID, sessionID)
	return s.TaskStore.WaitForResult(ctx, taskID, sessionID)
}

func newStallingTaskServer(t *testing.T, store *stallingStore, handler core.ToolHandler) *Server {
	t.Helper()
	srv := NewServer(core.Implementation{Name: "wakeup", Version: "0.0.1"})
	srv.RegisterTool(core.ToolDef{
		Name:        "work",
		InputSchema: map[string]any{"type": "object"},
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
	}, handler)
	RegisterTasksV1(TasksConfigV1{Server: srv, Store: store})
	return srv
}

// TestTaskResultSeesElicitationQueuedBeforeItWaits is issue 390 made
// deterministic. The tool elicits after tasks/result has checked the side
// channel and before it waits. A send on that channel is not a store update,
// so a handler waiting only on the store never wakes, and the tool blocks
// forever on the reply.
func TestTaskResultSeesElicitationQueuedBeforeItWaits(t *testing.T) {
	store := newStallingStore(func(s core.TaskStatus) bool { return s == core.TaskInputRequired })
	srv := newStallingTaskServer(t, store, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		<-store.parked
		res, err := GetTaskContext(ctx).TaskElicit(core.ElicitationRequest{Message: "ok?"})
		if err != nil {
			return core.TextResult("elicit failed: " + err.Error()), nil
		}
		return core.TextResult("elicit " + res.Action), nil
	})
	c := connectClient(t, srv, client.WithElicitationHandler(
		func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept"}, nil
		},
	))

	created, err := client.ToolCallAsTaskV1(t.Context(), c, "work", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := getTaskPayloadOrTimeout(t, c, created.Task.TaskID, 5*time.Second)
	if err != nil {
		t.Fatalf("GetTaskPayload: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "elicit accept" {
		t.Errorf("result = %+v, want \"elicit accept\"", result)
	}
}

// TestTaskResultSeesCompletionBeforeItWaits covers the same gap on the
// terminal path: a task that completes between the handler's status check
// and its wait was never seen as done.
func TestTaskResultSeesCompletionBeforeItWaits(t *testing.T) {
	store := newStallingStore(func(s core.TaskStatus) bool { return s.IsTerminal() })
	srv := newStallingTaskServer(t, store, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		<-store.parked
		return core.TextResult("done"), nil
	})
	c := connectClient(t, srv)

	created, err := client.ToolCallAsTaskV1(t.Context(), c, "work", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := getTaskPayloadOrTimeout(t, c, created.Task.TaskID, 5*time.Second)
	if err != nil {
		t.Fatalf("GetTaskPayload: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "done" {
		t.Errorf("result = %+v, want \"done\"", result)
	}
}

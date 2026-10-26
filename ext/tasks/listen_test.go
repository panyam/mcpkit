package tasks_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	server "github.com/panyam/mcpkit/server"
)

// ext-tasks' taskIds key on subscriptions/listen (#1523): a stateless client
// names the tasks it wants status for, the server keeps the ones the caller
// may read under the tasks/get binding, and notifications/tasks for those
// tasks arrive on the listen stream.

// newListenTaskServer serves a "gated" task tool that finishes when release
// is closed, behind a per-tenant task bucket keyer read from X-Tenant.
func newListenTaskServer(t *testing.T) (url string, release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	srv := server.NewServer(core.ServerInfo{Name: "listen-tasks", Version: "0.0.1"},
		server.WithTaskBucketKeyer(func(ctx context.Context) string {
			tenant, _ := ctx.Value(tenantCtxKey{}).(string)
			return tenant
		}))
	srv.RegisterTool(
		core.ToolDef{
			Name:        "gated",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return core.TextResult("done"), nil
		},
	)
	tasks.Register(tasks.Config{Server: srv, DefaultPollMs: 50})

	inner := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant := r.Header.Get("X-Tenant"); tenant != "" {
			r = r.WithContext(context.WithValue(r.Context(), tenantCtxKey{}, tenant))
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		ts.Close()
	})
	return ts.URL + "/mcp", release
}

func createGatedTask(t *testing.T, url, tenant string) string {
	t.Helper()
	resp := postAsTenant(t, url, tenant, 1, "tools/call", map[string]any{
		"name": "gated", "arguments": map[string]any{}, "_meta": metaWithCaps(true),
	})
	if resp.Error != nil {
		t.Fatalf("tenant %s create: %+v", tenant, resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got core.CreateTaskResult
	if err := json.Unmarshal(raw, &got); err != nil || got.TaskID == "" {
		t.Fatalf("tenant %s: no taskId in %s", tenant, raw)
	}
	return got.TaskID
}

// openTaskListen opens subscriptions/listen as tenant for taskIDs.
func openTaskListen(t *testing.T, ctx context.Context, url, tenant string, taskIDs []string, withTasks bool) *http.Response {
	t.Helper()
	params := map[string]any{"_meta": metaWithCaps(withTasks), "notifications": map[string]any{"taskIds": taskIDs}}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "listen-1", "method": "subscriptions/listen", "params": params})
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", statelessDraftVersion)
	req.Header.Set(core.McpMethodHeader, "subscriptions/listen")
	req.Header.Set("X-Tenant", tenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return resp
}

// streamFrames returns a listen stream's frames as they arrive.
func streamFrames(resp *http.Response) <-chan map[string]any {
	frames := make(chan map[string]any, 16)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var f map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &f) == nil {
				frames <- f
			}
		}
	}()
	return frames
}

func nextFrame(t *testing.T, frames <-chan map[string]any, within time.Duration) map[string]any {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("listen stream ended")
		}
		return f
	case <-time.After(within):
		return nil
	}
}

func TestListen_TaskIDsDeliverOnlyTheCallersTasks(t *testing.T) {
	url, release := newListenTaskServer(t)
	mine := createGatedTask(t, url, "alice")
	theirs := createGatedTask(t, url, "bob")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openTaskListen(t, ctx, url, "alice", []string{mine, theirs, "no-such-task"}, true)
	defer resp.Body.Close()
	frames := streamFrames(resp)

	ack := nextFrame(t, frames, 3*time.Second)
	if ack == nil || ack["method"] != "notifications/subscriptions/acknowledged" {
		t.Fatalf("first frame = %v, want the acknowledgement", ack)
	}
	ackParams, _ := ack["params"].(map[string]any)
	honored, _ := ackParams["notifications"].(map[string]any)
	if ids, _ := honored["taskIds"].([]any); len(ids) != 1 || ids[0] != mine {
		t.Errorf("acknowledged taskIds = %v, want only the caller's own task %s", honored["taskIds"], mine)
	}

	close(release)
	var sawMine bool
	for f := nextFrame(t, frames, 3*time.Second); f != nil; f = nextFrame(t, frames, 500*time.Millisecond) {
		if f["method"] != "notifications/tasks" {
			t.Errorf("unexpected frame on the listen stream: %v", f)
			continue
		}
		p, _ := f["params"].(map[string]any)
		switch p["taskId"] {
		case mine:
			if p["status"] == string(core.TaskCompleted) {
				sawMine = true
				if p["result"] == nil {
					t.Errorf("completed status for %s carries no result: %v", mine, p)
				}
			}
		case theirs:
			t.Errorf("another tenant's task %s reached this stream: %v", theirs, p)
		}
	}
	if !sawMine {
		t.Errorf("no completed notifications/tasks for %s on the listen stream", mine)
	}
}

func TestListen_TaskIDsWithoutTheExtensionIsRefused(t *testing.T) {
	url, _ := newListenTaskServer(t)
	mine := createGatedTask(t, url, "alice")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openTaskListen(t, ctx, url, "alice", []string{mine}, false)
	defer resp.Body.Close()

	// A refusal comes back as a plain JSON-RPC error, not a stream.
	var f map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		t.Fatalf("listen without the tasks extension: undecodable body (status %d): %v", resp.StatusCode, err)
	}
	errObj, _ := f["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(core.ErrCodeMissingRequiredClientCapability) {
		t.Fatalf("listen without the tasks extension = %v, want -32021", f)
	}
	raw, _ := json.Marshal(errObj["data"])
	if !strings.Contains(string(raw), core.TasksExtensionID) {
		t.Errorf("-32021 data %s does not name %s", raw, core.TasksExtensionID)
	}
}

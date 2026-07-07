package tasks_test

// Issue 485: multi-tenant isolation for the v2 task store on the SEP-2575
// stateless wire. Without a keyer, every stateless task keys under
// sessionID="" — so two tenants share one bucket and can read/cancel each
// other's tasks. server.WithTaskBucketKeyer derives the bucket from a
// per-request signal (here an X-Tenant header stashed on the context, standing
// in for an auth subject — no ext/auth dependency) so tenants are isolated.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/panyam/mcpkit/core"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	server "github.com/panyam/mcpkit/server"
)

type tenantCtxKey struct{}

// newIsolatedTaskServer builds a stateless task server. When withKeyer is true
// it installs a TaskBucketKeyer that reads the tenant from the context (set by
// the handler wrapper from the X-Tenant header). Returns the base URL.
func newIsolatedTaskServer(t *testing.T, withKeyer bool) string {
	t.Helper()

	opts := []server.Option{}
	if withKeyer {
		opts = append(opts, server.WithTaskBucketKeyer(func(ctx context.Context) string {
			tenant, _ := ctx.Value(tenantCtxKey{}).(string)
			return tenant
		}))
	}
	srv := server.NewServer(core.ServerInfo{Name: "iso-tasks", Version: "0.0.1"}, opts...)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_compute",
			Description: "optional task support",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("done"), nil
		},
	)
	tasks.Register(tasks.Config{Server: srv, DefaultPollMs: 50})

	inner := srv.Handler(server.WithStreamableHTTP(true))
	// Stand-in for auth: copy the X-Tenant header onto the request context so
	// the keyer can read it. A real deployment reads the JWT subject instead.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant := r.Header.Get("X-Tenant"); tenant != "" {
			r = r.WithContext(context.WithValue(r.Context(), tenantCtxKey{}, tenant))
		}
		inner.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func postAsTenant(t *testing.T, url, tenant string, id int, method string, params map[string]any) *core.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	req.Header.Set("MCP-Protocol-Version", statelessDraftVersion)
	req.Header.Set("X-Tenant", tenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	payload := bytes.TrimSpace(raw)
	if idx := bytes.Index(payload, []byte("data:")); idx >= 0 {
		payload = bytes.TrimSpace(payload[idx+len("data:"):])
	}
	var out core.Response
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode response (status %d): %v\n%s", resp.StatusCode, err, string(raw))
	}
	return &out
}

func createTaskAsTenant(t *testing.T, url, tenant string) string {
	t.Helper()
	resp := postAsTenant(t, url, tenant, 1, "tools/call", map[string]any{
		"name":      "slow_compute",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(true),
	})
	if resp.Error != nil {
		t.Fatalf("tenant %s create: %+v", tenant, resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got core.CreateTaskResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode CreateTaskResult: %v\n%s", err, string(raw))
	}
	if got.TaskID == "" {
		t.Fatalf("tenant %s: empty taskId (result=%s)", tenant, string(raw))
	}
	return got.TaskID
}

// TestIssue485_KeyerIsolatesTenants: with the keyer, tenant B cannot see a task
// tenant A created, but tenant A can.
func TestIssue485_KeyerIsolatesTenants(t *testing.T) {
	url := newIsolatedTaskServer(t, true /* withKeyer */)
	taskID := createTaskAsTenant(t, url, "tenant-A")

	// Tenant B must NOT find tenant A's task.
	respB := postAsTenant(t, url, "tenant-B", 2, "tasks/get", map[string]any{
		"taskId": taskID, "_meta": metaWithCaps(true),
	})
	if respB.Error == nil {
		t.Errorf("tenant-B should NOT see tenant-A's task %s, but tasks/get succeeded: %s", taskID, string(mustJSON(respB.Result)))
	}

	// Tenant A must still find its own task.
	respA := postAsTenant(t, url, "tenant-A", 3, "tasks/get", map[string]any{
		"taskId": taskID, "_meta": metaWithCaps(true),
	})
	if respA.Error != nil {
		t.Errorf("tenant-A should see its own task %s, got error: %+v", taskID, respA.Error)
	}
}

// TestIssue485_DefaultSharesBucket documents the default (no keyer): both
// tenants key under sessionID="" on the stateless wire, so tenant B CAN see
// tenant A's task. This is the isolation hole the keyer closes.
func TestIssue485_DefaultSharesBucket(t *testing.T) {
	url := newIsolatedTaskServer(t, false /* no keyer */)
	taskID := createTaskAsTenant(t, url, "tenant-A")

	respB := postAsTenant(t, url, "tenant-B", 2, "tasks/get", map[string]any{
		"taskId": taskID, "_meta": metaWithCaps(true),
	})
	if respB.Error != nil {
		t.Errorf("default (no keyer): tenant-B should share the sessionID=\"\" bucket and see the task, got error: %+v", respB.Error)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

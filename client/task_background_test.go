package client_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

type bgTaskFixture struct {
	mu   sync.Mutex
	hold chan struct{}
	done bool
}

func (f *bgTaskFixture) server(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "bg-fixture", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "job", Description: "long job", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.CreateTaskResult{ResultType: core.ResultTypeTask, TaskInfoV2: core.TaskInfoV2{
				TaskID: "bg-1", Status: core.TaskWorking, CreatedAt: "x", LastUpdatedAt: "x",
				TTLMs: core.IntPtr(60000), PollIntervalMs: core.IntPtr(1),
			}}, nil
		},
	)
	srv.HandleMethod("tasks/get", func(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		dt := core.DetailedTask{TaskInfoV2: core.TaskInfoV2{TaskID: "bg-1", CreatedAt: "x", LastUpdatedAt: "x", TTLMs: core.IntPtr(60000), PollIntervalMs: core.IntPtr(1)}}
		select {
		case <-f.hold:
			dt.Status = core.TaskCompleted
			dt.Result = &core.ToolResult{Content: []core.Content{{Type: "text", Text: "done"}}}
		default:
			dt.Status = core.TaskWorking
		}
		return &core.Response{JSONRPC: "2.0", ID: id, Result: dt}
	})
	return srv
}

func noInput(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
	return nil, nil
}

func TestWaitForTaskOrBackground_DetachAndComplete(t *testing.T) {
	f := &bgTaskFixture{hold: make(chan struct{})}
	ts := httptest.NewServer(f.server(t).Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "bg-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	dt, bt, err := client.WaitForTaskOrBackground(context.Background(), c, "bg-1", "job", noInput, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if dt != nil || bt == nil {
		t.Fatalf("still-working task must detach: dt=%v bt=%v", dt, bt)
	}
	if bt.Status() != core.TaskWorking || bt.Tool != "job" {
		t.Fatalf("handle = %+v status=%v", bt, bt.Status())
	}

	close(f.hold)
	select {
	case <-bt.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("background poll never completed")
	}
	res, rerr := bt.Result()
	if rerr != nil || res.Status != core.TaskCompleted || res.Result.Content[0].Text != "done" {
		t.Fatalf("background result = %+v %v", res, rerr)
	}
}

func TestWaitForTaskOrBackground_InlineWhenFast(t *testing.T) {
	f := &bgTaskFixture{hold: make(chan struct{})}
	close(f.hold) // completes on the first poll
	ts := httptest.NewServer(f.server(t).Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "bg-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	dt, bt, err := client.WaitForTaskOrBackground(context.Background(), c, "bg-1", "job", noInput, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if bt != nil || dt == nil || dt.Status != core.TaskCompleted {
		t.Fatalf("fast task must finish inline: dt=%v bt=%v", dt, bt)
	}
}

func TestWaitForTaskOrBackground_ZeroGraceNeverDetaches(t *testing.T) {
	f := &bgTaskFixture{hold: make(chan struct{})}
	close(f.hold)
	ts := httptest.NewServer(f.server(t).Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "bg-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	_, bt, err := client.WaitForTaskOrBackground(context.Background(), c, "bg-1", "job", noInput, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bt != nil {
		t.Fatal("grace 0 must behave like WaitForTaskWithInput (never detaches)")
	}
}

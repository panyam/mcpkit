package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func TestSelectorNarrowsOfferedTools(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "relevant", "", func(ctx context.Context, _ struct{}) (string, error) { return "ok", nil })
	AddFunc(src, "irrelevant", "", func(ctx context.Context, _ struct{}) (string, error) { return "no", nil })

	stub := NewStubProvider(StubTurn{Text: "done"})
	r, err := NewRunner(RunnerConfig{
		Provider: stub,
		Tools:    src,
		Selector: func(ctx context.Context, history []Message, tools []core.ToolDef) ([]core.ToolDef, error) {
			var out []core.ToolDef
			for _, td := range tools {
				if strings.Contains(history[len(history)-1].Text, td.Name) {
					out = append(out, td)
				}
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "use the relevant tool"}}, nil); err != nil {
		t.Fatal(err)
	}
	offered := stub.Requests()[0].Tools
	if len(offered) != 1 || offered[0].Name != "relevant" {
		t.Fatalf("selector must narrow by history, offered %v", toolNames(offered))
	}
}

func TestSelectorErrorAbortsTurn(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "x", "", func(ctx context.Context, _ struct{}) (string, error) { return "", nil })
	boom := errors.New("selector broke")
	r, _ := NewRunner(RunnerConfig{
		Provider: NewStubProvider(StubTurn{Text: "unreached"}),
		Tools:    src,
		Selector: func(ctx context.Context, h []Message, tools []core.ToolDef) ([]core.ToolDef, error) {
			return nil, boom
		},
	})
	if _, err := r.Run(context.Background(), nil, nil); !errors.Is(err, boom) {
		t.Fatalf("want selector error to abort, got %v", err)
	}
}

func TestSelectorSeesFreshListEachStep(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "t1", "", func(ctx context.Context, _ struct{}) (string, error) {
		AddFunc(src, "t2", "", func(ctx context.Context, _ struct{}) (string, error) { return "2", nil })
		return "1", nil
	})
	var perStep [][]string
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "t1", Args: json.RawMessage(`{}`)}}},
		StubTurn{Text: "done"},
	)
	r, _ := NewRunner(RunnerConfig{
		Provider: stub,
		Tools:    src,
		Selector: func(ctx context.Context, h []Message, tools []core.ToolDef) ([]core.ToolDef, error) {
			perStep = append(perStep, toolNames(tools))
			return tools, nil
		},
	})
	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(perStep) != 2 || len(perStep[0]) != 1 || len(perStep[1]) != 2 {
		t.Fatalf("selector must see the fresh list per step: %v", perStep)
	}
}

func TestFilterSourceIsACapabilityBoundary(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "allowed", "", func(ctx context.Context, _ struct{}) (string, error) { return "ok", nil })
	AddFunc(src, "blocked", "", func(ctx context.Context, _ struct{}) (string, error) { return "secret", nil })

	f := NewFilterSource(src, func(d core.ToolDef) bool { return d.Name != "blocked" })

	tools, err := f.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(toolNames(tools)) != "[allowed]" {
		t.Fatalf("tools = %v", toolNames(tools))
	}
	if _, err := f.Call(context.Background(), "allowed", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(context.Background(), "blocked", map[string]any{}); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("filtered tool must not be callable by name-guessing, got %v", err)
	}
}

func TestFilterSourceComposesUnderMultiSource(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "a", "", func(ctx context.Context, _ struct{}) (string, error) { return "", nil })
	AddFunc(src, "b", "", func(ctx context.Context, _ struct{}) (string, error) { return "", nil })
	m := NewMultiSource()
	m.Add("f", NewFilterSource(src, func(d core.ToolDef) bool { return d.Name == "a" }))
	tools, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(toolNames(tools)) != "[a]" {
		t.Fatalf("tools = %v", toolNames(tools))
	}
}

func TestListChangedInvalidatesMultiSourceIndex(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	m := NewMultiSource()
	var fired atomic.Int32
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "inv-test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithToolsListChangedHandler(func() {
			fired.Add(1)
			m.Invalidate()
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if err := m.Add("srv", NewClientSource(c)); err != nil {
		t.Fatal(err)
	}

	before, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if containsTool(before, "runtime_tool") {
		t.Fatal("fixture assumption broken")
	}

	srv.RegisterTool(
		core.ToolDef{Name: "runtime_tool", Description: "added later", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("rt"), nil
		},
	)

	deadline := time.Now().Add(3 * time.Second)
	for fired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fired.Load() == 0 {
		t.Fatal("list_changed never reached the handler")
	}

	after, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !containsTool(after, "runtime_tool") {
		t.Fatalf("Tools() must reflect the new tool after Invalidate (no Call-miss needed), got %v", toolNames(after))
	}
}

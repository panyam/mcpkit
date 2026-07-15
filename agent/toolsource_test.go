package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

type fakeSource struct {
	defs    []core.ToolDef
	calls   []string
	listErr error
}

func (f *fakeSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.defs, nil
}

func (f *fakeSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	f.calls = append(f.calls, name)
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "from-fake:" + name}}}, nil
}

func defsNamed(names ...string) []core.ToolDef {
	out := make([]core.ToolDef, len(names))
	for i, n := range names {
		out[i] = core.ToolDef{Name: n, Description: "d-" + n, InputSchema: map[string]any{"type": "object"}}
	}
	return out
}

func TestMultiSourceMergesUniqueNames(t *testing.T) {
	m := NewMultiSource()
	if err := m.Add("alpha", &fakeSource{defs: defsNamed("one", "two")}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("beta", &fakeSource{defs: defsNamed("three")}); err != nil {
		t.Fatal(err)
	}
	tools, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := toolNames(tools)
	want := []string{"one", "two", "three"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestMultiSourceCollisionQualifiesAllClaimants(t *testing.T) {
	var notified []string
	m := NewMultiSource(WithCollisionNotify(func(name string, ids []string) {
		notified = append(notified, fmt.Sprintf("%s:%v", name, ids))
	}))
	m.Add("beta", &fakeSource{defs: defsNamed("search", "solo")})
	m.Add("alpha", &fakeSource{defs: defsNamed("search")})

	tools, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := toolNames(tools)
	// Qualified forms sorted by sourceID regardless of add order; bare
	// "search" must not appear.
	want := []string{"alpha_search", "beta_search", "solo"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if len(notified) != 1 || !strings.Contains(notified[0], "search") {
		t.Fatalf("collision notify = %v", notified)
	}
}

func TestMultiSourceCallBareUniqueName(t *testing.T) {
	src := &fakeSource{defs: defsNamed("only")}
	m := NewMultiSource()
	m.Add("alpha", src)
	res, err := m.Call(context.Background(), "only", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0].Text != "from-fake:only" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestMultiSourceCallAmbiguousWithoutResolverFails(t *testing.T) {
	m := NewMultiSource()
	m.Add("alpha", &fakeSource{defs: defsNamed("search")})
	m.Add("beta", &fakeSource{defs: defsNamed("search")})
	_, err := m.Call(context.Background(), "search", nil)
	if err == nil || !strings.Contains(err.Error(), "alpha_search") || !strings.Contains(err.Error(), "beta_search") {
		t.Fatalf("want ambiguity error naming qualified forms, got %v", err)
	}
}

func TestMultiSourceCallAmbiguousWithResolver(t *testing.T) {
	alpha := &fakeSource{defs: defsNamed("search")}
	beta := &fakeSource{defs: defsNamed("search")}
	m := NewMultiSource(WithResolver(func(name string, candidates []ToolOwner, args map[string]any) (string, error) {
		if len(candidates) != 2 {
			return "", fmt.Errorf("want 2 candidates, got %d", len(candidates))
		}
		return "beta", nil
	}))
	m.Add("alpha", alpha)
	m.Add("beta", beta)
	if _, err := m.Call(context.Background(), "search", nil); err != nil {
		t.Fatal(err)
	}
	if len(beta.calls) != 1 || len(alpha.calls) != 0 {
		t.Fatalf("resolver routing failed: alpha=%v beta=%v", alpha.calls, beta.calls)
	}
}

func TestMultiSourceCallQualifiedName(t *testing.T) {
	alpha := &fakeSource{defs: defsNamed("search")}
	m := NewMultiSource()
	m.Add("alpha", alpha)
	m.Add("beta", &fakeSource{defs: defsNamed("search")})
	if _, err := m.Call(context.Background(), "alpha_search", nil); err != nil {
		t.Fatal(err)
	}
	if len(alpha.calls) != 1 || alpha.calls[0] != "search" {
		t.Fatalf("qualified call must dispatch bare name to owner, got %v", alpha.calls)
	}
}

func TestMultiSourceRejectsUnderscoreIDs(t *testing.T) {
	m := NewMultiSource()
	if err := m.Add("bad_id", &fakeSource{}); err == nil {
		t.Fatal("want error for underscore in source id")
	}
}

func TestMultiSourceRemove(t *testing.T) {
	m := NewMultiSource()
	m.Add("alpha", &fakeSource{defs: defsNamed("one")})
	m.Remove("alpha")
	tools, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools after remove = %v", tools)
	}
	if _, err := m.Call(context.Background(), "one", nil); err == nil {
		t.Fatal("want unknown-tool error after remove")
	}
}

func TestMultiSourceListErrorPropagates(t *testing.T) {
	m := NewMultiSource()
	m.Add("alpha", &fakeSource{listErr: errors.New("boom")})
	if _, err := m.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("want list error naming the source, got %v", err)
	}
}

func TestFuncSourceTypedRegistrationAndCall(t *testing.T) {
	type addInput struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	s := NewFuncSource()
	if err := AddFunc(s, "add", "Adds two ints", func(ctx context.Context, in addInput) (string, error) {
		return fmt.Sprintf("%d", in.A+in.B), nil
	}); err != nil {
		t.Fatal(err)
	}

	tools, err := s.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "add" {
		t.Fatalf("tools = %+v", tools)
	}
	schema, ok := tools[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("schema type %T", tools[0].InputSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["a"]; !ok {
		t.Fatalf("generated schema missing property a: %v", schema)
	}

	res, err := s.Call(context.Background(), "add", map[string]any{"a": 2, "b": 40})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].Text != "42" {
		t.Fatalf("result = %+v", res)
	}
}

func TestFuncSourceHandlerErrorBecomesIsError(t *testing.T) {
	s := NewFuncSource()
	AddFunc(s, "boom", "always fails", func(ctx context.Context, in struct{}) (string, error) {
		return "", errors.New("kaput")
	})
	res, err := s.Call(context.Background(), "boom", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "kaput") {
		t.Fatalf("want IsError result carrying the message, got %+v", res)
	}
}

func TestFuncSourceBadArgsBecomeIsError(t *testing.T) {
	type in struct {
		N int `json:"n"`
	}
	s := NewFuncSource()
	AddFunc(s, "typed", "wants an int", func(ctx context.Context, i in) (string, error) { return "ok", nil })
	res, err := s.Call(context.Background(), "typed", map[string]any{"n": "not-a-number"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("want IsError for undecodable args, got %+v", res)
	}
}

func TestFuncSourceDuplicateAndUnknown(t *testing.T) {
	s := NewFuncSource()
	AddFunc(s, "x", "", func(ctx context.Context, _ struct{}) (string, error) { return "", nil })
	if err := AddFunc(s, "x", "", func(ctx context.Context, _ struct{}) (string, error) { return "", nil }); err == nil {
		t.Fatal("want duplicate-name error")
	}
	if _, err := s.Call(context.Background(), "nope", nil); err == nil {
		t.Fatal("want unknown-tool dispatch error")
	}
}

func TestClientSourceAgainstInProcessServer(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "agent-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	src := NewClientSource(c)

	tools, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !containsTool(tools, "echo") || !containsTool(tools, "fail") {
		t.Fatalf("tools = %v", toolNames(tools))
	}

	res, err := src.Call(context.Background(), "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "echo: hi") {
		t.Fatalf("echo result = %+v", res)
	}

	failRes, err := src.Call(context.Background(), "fail", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !failRes.IsError {
		t.Fatalf("fail tool must surface as IsError result, got %+v", failRes)
	}
}

func TestClientSourceComposesUnderMultiSource(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "agent-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	local := NewFuncSource()
	AddFunc(local, "today", "returns a fixed date", func(ctx context.Context, _ struct{}) (string, error) {
		return "2026-07-15", nil
	})

	m := NewMultiSource()
	if err := m.Add("srv", NewClientSource(c)); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("local", local); err != nil {
		t.Fatal(err)
	}

	res, err := m.Call(context.Background(), "today", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0].Text != "2026-07-15" {
		t.Fatalf("local tool through multi = %+v", res)
	}
	res, err = m.Call(context.Background(), "echo", map[string]any{"message": "via-multi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "via-multi") {
		t.Fatalf("server tool through multi = %+v", res)
	}
}

func toolNames(defs []core.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

func containsTool(defs []core.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

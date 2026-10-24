package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

func parityServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	s := statelessServerWithResources(t, opts...)
	for _, name := range []string{"keep", "hide"} {
		s.RegisterTool(core.ToolDef{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
	}
	s.RegisterPrompt(core.PromptDef{Name: "greet", Arguments: []core.PromptArgument{{Name: "who"}}},
		func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		})
	s.RegisterCompletion("ref/prompt", "greet",
		func(core.PromptContext, core.CompletionRef, core.CompletionArgument) (core.CompletionResult, error) {
			return core.CompletionResult{Values: []string{"world"}}, nil
		})
	return s
}

func statelessCall(t *testing.T, url, method string, params map[string]any) *core.Response {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = statelessMeta()
	return decode(t, postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	}, map[string]string{mcpProtocolVersionHeader: draftVersion}))
}

var enumerationCalls = []struct {
	method string
	params map[string]any
}{
	{"tools/list", nil},
	{"resources/list", nil},
	{"resources/templates/list", nil},
	{"prompts/list", nil},
	{"completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "greet"},
		"argument": map[string]any{"name": "who", "value": "w"},
	}},
}

// Each of these five skipped the middleware chain on the stateless wire, so a
// middleware saw them on the session wire only (#1355).
func TestStatelessMiddleware_SeesEnumerationAndCompletion(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	record := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		mu.Lock()
		seen = append(seen, req.Method)
		mu.Unlock()
		return next(ctx, req)
	}
	s := parityServer(t, WithMiddleware(record))
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	defer ts.Close()

	for _, c := range enumerationCalls {
		t.Run(c.method, func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()
			resp := statelessCall(t, ts.URL+"/mcp", c.method, c.params)
			if resp.Error != nil {
				t.Fatalf("%s: error %d %s", c.method, resp.Error.Code, resp.Error.Message)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Contains(seen, c.method) {
				t.Errorf("middleware saw %v, want %s", seen, c.method)
			}
		})
	}
}

// The chain must not change what the lists and completion answer, including
// the SEP-2549 cache hints the stateless handlers used to attach themselves.
func TestStatelessMiddleware_ResultsUnchanged(t *testing.T) {
	passthrough := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		return next(ctx, req)
	}
	s := parityServer(t, WithMiddleware(passthrough), WithListCacheControl(5000, core.CacheScopePrivate))
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	defer ts.Close()

	want := map[string]int{"tools/list": 2, "resources/list": 1, "resources/templates/list": 1, "prompts/list": 1}
	keys := map[string]string{"tools/list": "tools", "resources/list": "resources", "resources/templates/list": "resourceTemplates", "prompts/list": "prompts"}
	for _, c := range enumerationCalls {
		t.Run(c.method, func(t *testing.T) {
			resp := statelessCall(t, ts.URL+"/mcp", c.method, c.params)
			if resp.Error != nil {
				t.Fatalf("error %d %s", resp.Error.Code, resp.Error.Message)
			}
			var result map[string]json.RawMessage
			raw, _ := json.Marshal(resp.Result)
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatalf("result: %v", err)
			}
			if c.method == "completion/complete" {
				var cr core.CompletionCompleteResult
				_ = json.Unmarshal(raw, &cr)
				if !slices.Equal(cr.Completion.Values, []string{"world"}) {
					t.Errorf("completion values = %v, want [world]", cr.Completion.Values)
				}
				return
			}
			var items []json.RawMessage
			_ = json.Unmarshal(result[keys[c.method]], &items)
			if len(items) != want[c.method] {
				t.Errorf("%s has %d entries, want %d: %s", keys[c.method], len(items), want[c.method], raw)
			}
			if string(result["ttlMs"]) != "5000" || string(result["cacheScope"]) != `"private"` {
				t.Errorf("cache hints ttlMs=%s cacheScope=%s, want 5000 and \"private\"", result["ttlMs"], result["cacheScope"])
			}
		})
	}
}

// A middleware that rewrites tools/list must produce the same list on both
// wires. Before #1355 the stateless wire showed the unfiltered registry.
func TestStatelessMiddleware_ToolsListFilterMatchesSessionWire(t *testing.T) {
	hide := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		resp, err := next(ctx, req)
		if req.Method != "tools/list" || err != nil || resp == nil || resp.Error != nil {
			return resp, err
		}
		if r, ok := resp.Result.(core.ToolsListResult); ok {
			r.Tools = slices.DeleteFunc(slices.Clone(r.Tools), func(d core.ToolDef) bool { return d.Name == "hide" })
			resp.Result = r
		}
		return resp, err
	}
	s := parityServer(t, WithMiddleware(hide))
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeDual)))
	defer ts.Close()

	names := func(resp *core.Response) []string {
		t.Helper()
		if resp.Error != nil {
			t.Fatalf("tools/list error %d %s", resp.Error.Code, resp.Error.Message)
		}
		var r core.ToolsListResult
		raw, _ := json.Marshal(resp.Result)
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("tools/list result: %v", err)
		}
		var out []string
		for _, d := range r.Tools {
			out = append(out, d.Name)
		}
		slices.Sort(out)
		return out
	}

	sid := streamableInit(t, ts.URL)
	sessionResp, err := streamablePost(ts.URL+"/mcp", sid, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if err != nil {
		t.Fatalf("session tools/list: %v", err)
	}
	session := names(decode(t, sessionResp))
	stateless := names(statelessCall(t, ts.URL+"/mcp", "tools/list", nil))

	if !slices.Equal(session, []string{"keep"}) {
		t.Fatalf("session wire lists %v, want [keep]; the filter itself is not working", session)
	}
	if !slices.Equal(stateless, session) {
		t.Errorf("stateless wire lists %v, session wire lists %v", stateless, session)
	}
}

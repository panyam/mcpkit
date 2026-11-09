package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// The wire-parity exercise for mission #1552 (`make exercise-wire`). A
// 2026-07-28 client sends the same requests over the session wire and the
// SEP-2575 stateless wire, and each row checks one thing the mission needs
// both wires to agree on. Row names lead with the ticket that made them.

const parityInstructions = "Call upload_report with the quarterly PDF."

// parityWire is one way of reaching the server: a session negotiated at
// 2026-07-28, or the stateless wire. call returns the JSON result object, or
// the JSON-RPC error.
type parityWire struct {
	name string
	call func(t *testing.T, method string, params map[string]any) (map[string]json.RawMessage, *core.Error)
	// callHeaders is call with extra HTTP headers, and it also returns the
	// HTTP status.
	callHeaders func(t *testing.T, method string, params map[string]any, headers map[string]string) (int, map[string]json.RawMessage, *core.Error)
	// initResult is the initialize result on the session wire and the
	// server/discover result on the stateless wire.
	initResult map[string]json.RawMessage
	// notify posts method as a notification, with no id member or with
	// "id":null, and returns the HTTP status and body.
	notify func(t *testing.T, method string, nullID bool) (int, string)
}

func parityFixture(t *testing.T, opts ...Option) *Server {
	t.Helper()
	s := statelessServerWithResources(t, append([]Option{WithInstructions(parityInstructions)}, opts...)...)
	s.RegisterTool(core.ToolDef{
		Name: "upload_report",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"report": core.FileInputProperty(core.FileInputDescriptor{Accept: []string{"application/pdf"}}),
			},
		},
	}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
	s.RegisterPrompt(core.PromptDef{Name: "greet", Arguments: []core.PromptArgument{{Name: "who"}}},
		func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{Messages: []core.PromptMessage{{Role: "user", Content: core.Content{Type: "text", Text: "hi"}}}}, nil
		})
	s.RegisterCompletion("ref/prompt", "greet",
		func(core.PromptContext, core.CompletionRef, core.CompletionArgument) (core.CompletionResult, error) {
			return core.CompletionResult{Values: []string{"world"}}, nil
		})
	return s
}

// parityWires serves s twice, once per wire, and returns both, each
// declaring caps as its client capabilities. It uses two single-wire handlers
// rather than one dual-mode one, because dual mode routes any request whose
// _meta carries a protocolVersion to the stateless dispatcher, session id or
// not, so a 2026-07-28 session request never reaches the session wire there.
func parityWires(t *testing.T, s *Server, caps map[string]any) []parityWire {
	t.Helper()
	sessionTS := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeLegacyOnly)))
	t.Cleanup(sessionTS.Close)
	statelessTS := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	t.Cleanup(statelessTS.Close)
	sessionURL, statelessURL := sessionTS.URL+"/mcp", statelessTS.URL+"/mcp"
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    draftVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "parity", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": caps,
	}
	var nextID atomic.Int64

	post := func(t *testing.T, sid, method string, params map[string]any, extra map[string]string) (*core.Response, string, int) {
		t.Helper()
		if params == nil {
			params = map[string]any{}
		}
		if method != "initialize" {
			params["_meta"] = meta
		}
		body := map[string]any{"jsonrpc": "2.0", "id": nextID.Add(1), "method": method, "params": params}
		headers := map[string]string{}
		if method != "initialize" {
			headers[mcpProtocolVersionHeader] = draftVersion
		}
		if sid != "" {
			headers[mcpSessionIDHeader] = sid
		}
		for k, v := range extra {
			headers[k] = v
		}
		url := statelessURL
		if sid != "" || method == "initialize" {
			url = sessionURL
		}
		httpResp := postStatelessJSON(t, url, body, headers)
		gotSID := httpResp.Header.Get(mcpSessionIDHeader)
		return decode(t, httpResp), gotSID, httpResp.StatusCode
	}
	result := func(t *testing.T, resp *core.Response) (map[string]json.RawMessage, *core.Error) {
		t.Helper()
		if resp.Error != nil {
			return nil, resp.Error
		}
		raw, _ := json.Marshal(resp.Result)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("result is not an object: %s", raw)
		}
		return out, nil
	}

	initResp, sid, _ := post(t, "", "initialize", map[string]any{
		"protocolVersion": draftVersion,
		"capabilities":    caps,
		"clientInfo":      map[string]any{"name": "parity", "version": "1"},
	}, nil)
	initResult, rpcErr := result(t, initResp)
	if rpcErr != nil || sid == "" {
		t.Fatalf("initialize at %s: err=%v sid=%q", draftVersion, rpcErr, sid)
	}
	ack, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req, _ := http.NewRequest(http.MethodPost, sessionURL, strings.NewReader(string(ack)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	req.Header.Set(mcpSessionIDHeader, sid)
	req.Header.Set(mcpProtocolVersionHeader, draftVersion)
	stampRoutingHeaders(t, req, ack)
	if r, err := http.DefaultClient.Do(req); err == nil {
		r.Body.Close()
	}

	session := parityWire{name: "session", initResult: initResult}
	callOn := func(sid string) func(t *testing.T, method string, params map[string]any, headers map[string]string) (int, map[string]json.RawMessage, *core.Error) {
		return func(t *testing.T, method string, params map[string]any, headers map[string]string) (int, map[string]json.RawMessage, *core.Error) {
			resp, _, status := post(t, sid, method, params, headers)
			res, rpcErr := result(t, resp)
			return status, res, rpcErr
		}
	}
	stateless := parityWire{name: "stateless"}
	session.callHeaders, stateless.callHeaders = callOn(sid), callOn("")
	for _, w := range []*parityWire{&session, &stateless} {
		callHeaders := w.callHeaders
		w.call = func(t *testing.T, method string, params map[string]any) (map[string]json.RawMessage, *core.Error) {
			_, res, rpcErr := callHeaders(t, method, params, nil)
			return res, rpcErr
		}
	}
	discover, rpcErr := stateless.call(t, "server/discover", nil)
	if rpcErr != nil {
		t.Fatalf("server/discover: %v", rpcErr)
	}
	stateless.initResult = discover
	notifyOn := func(sid string) func(t *testing.T, method string, nullID bool) (int, string) {
		return func(t *testing.T, method string, nullID bool) (int, string) {
			t.Helper()
			body := map[string]any{"jsonrpc": "2.0", "method": method, "params": map[string]any{"_meta": meta}}
			if nullID {
				body["id"] = nil
			}
			headers := map[string]string{mcpProtocolVersionHeader: draftVersion}
			url := statelessURL
			if sid != "" {
				headers[mcpSessionIDHeader] = sid
				url = sessionURL
			}
			resp := postStatelessJSON(t, url, body, headers)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
	}
	session.notify, stateless.notify = notifyOn(sid), notifyOn("")
	return []parityWire{session, stateless}
}

// parityCalls is every request-bearing method both wires serve, with params
// the fixture answers.
var parityCalls = []struct {
	method string
	params map[string]any
	// cacheable is true for the results the 2026-07-28 schema derives from
	// CacheableResult, which makes ttlMs and cacheScope required.
	cacheable bool
}{
	{"tools/list", nil, true},
	{"tools/call", map[string]any{"name": "upload_report", "arguments": map[string]any{}}, false},
	{"resources/list", nil, true},
	{"resources/read", map[string]any{"uri": "test://static"}, true},
	{"resources/templates/list", nil, true},
	{"prompts/list", nil, true},
	{"prompts/get", map[string]any{"name": "greet", "arguments": map[string]any{"who": "w"}}, false},
	{"completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "greet"},
		"argument": map[string]any{"name": "who", "value": "w"},
	}, false},
}

func cloneParams(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

func TestWireParity(t *testing.T) {
	t.Run("1355 middleware sees every method", func(t *testing.T) {
		var mu sync.Mutex
		var seen []string
		record := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
			mu.Lock()
			seen = append(seen, req.Method)
			mu.Unlock()
			return next(ctx, req)
		}
		for _, w := range parityWires(t, parityFixture(t, WithMiddleware(record)), map[string]any{}) {
			for _, c := range parityCalls {
				mu.Lock()
				seen = nil
				mu.Unlock()
				if _, rpcErr := w.call(t, c.method, cloneParams(c.params)); rpcErr != nil {
					t.Fatalf("%s %s: %v", w.name, c.method, rpcErr)
				}
				mu.Lock()
				if !slices.Contains(seen, c.method) {
					t.Errorf("%s wire: middleware saw %v for %s", w.name, seen, c.method)
				}
				mu.Unlock()
			}
		}
	})

	t.Run("1515 x-mcp-file only for clients that declare fileInputs", func(t *testing.T) {
		for _, tc := range []struct {
			caps map[string]any
			want bool
		}{
			{map[string]any{}, false},
			{map[string]any{"fileInputs": map[string]any{}}, true},
		} {
			for _, w := range parityWires(t, parityFixture(t), tc.caps) {
				res, rpcErr := w.call(t, "tools/list", nil)
				if rpcErr != nil {
					t.Fatalf("%s tools/list: %v", w.name, rpcErr)
				}
				if got := strings.Contains(string(res["tools"]), core.FileInputSchemaKey); got != tc.want {
					t.Errorf("%s wire, caps %v: x-mcp-file present=%v, want %v", w.name, tc.caps, got, tc.want)
				}
			}
		}
	})

	t.Run("1546 ttlMs and cacheScope on every cacheable result", func(t *testing.T) {
		for _, cfg := range []struct {
			name string
			opts []Option
		}{
			{"defaults", nil},
			{"cache hints turned off", []Option{WithoutListCacheControl(), WithoutReadResourceCacheControl()}},
		} {
			for _, w := range parityWires(t, parityFixture(t, cfg.opts...), map[string]any{}) {
				check := func(what string, res map[string]json.RawMessage) {
					var ttl int
					if err := json.Unmarshal(res["ttlMs"], &ttl); err != nil || ttl < 0 {
						t.Errorf("%s, %s wire, %s: ttlMs = %s, want a number >= 0", cfg.name, w.name, what, res["ttlMs"])
					}
					var scope string
					_ = json.Unmarshal(res["cacheScope"], &scope)
					if scope != core.CacheScopePublic && scope != core.CacheScopePrivate {
						t.Errorf("%s, %s wire, %s: cacheScope = %s, want public or private", cfg.name, w.name, what, res["cacheScope"])
					}
				}
				if w.name == "stateless" {
					check("server/discover", w.initResult)
				}
				for _, c := range parityCalls {
					if !c.cacheable {
						continue
					}
					res, rpcErr := w.call(t, c.method, cloneParams(c.params))
					if rpcErr != nil {
						t.Fatalf("%s %s: %v", w.name, c.method, rpcErr)
					}
					check(c.method, res)
				}
			}
		}
	})

	t.Run("1560 instructions at the top level", func(t *testing.T) {
		for _, w := range parityWires(t, parityFixture(t), map[string]any{}) {
			var got string
			_ = json.Unmarshal(w.initResult["instructions"], &got)
			if got != parityInstructions {
				t.Errorf("%s wire: top-level instructions = %q, want %q", w.name, got, parityInstructions)
			}
			if strings.Contains(string(w.initResult["serverInfo"]), "instructions") {
				t.Errorf("%s wire: serverInfo carries instructions: %s", w.name, w.initResult["serverInfo"])
			}
			if strings.Contains(string(w.initResult["_meta"]), "instructions") {
				t.Errorf("%s wire: _meta carries instructions: %s", w.name, w.initResult["_meta"])
			}
		}
	})

	t.Run("1637 a stray tool argument is a tool error", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			opts     []Option
			wantRuns bool
		}{
			{"validation on", nil, false},
			{"validation off", []Option{WithSchemaValidation(false)}, true},
		} {
			var runs atomic.Int64
			for _, w := range parityWires(t, handlerParityFixture(t, &runs, tc.opts...), map[string]any{}) {
				runs.Store(0)
				res, rpcErr := w.call(t, "tools/call", map[string]any{
					"name": "strict", "arguments": map[string]any{"design": "x", "desing": "y"},
				})
				if rpcErr != nil {
					t.Fatalf("%s, %s wire: %v", tc.name, w.name, rpcErr)
				}
				isError := string(res["isError"]) == "true"
				if isError == tc.wantRuns || (runs.Load() > 0) != tc.wantRuns {
					t.Errorf("%s, %s wire: isError=%v, handler ran %d times; want handler to run: %v",
						tc.name, w.name, isError, runs.Load(), tc.wantRuns)
				}
			}
		}
	})

	t.Run("1637 prompt arguments are checked against their schema", func(t *testing.T) {
		for _, w := range parityWires(t, handlerParityFixture(t, new(atomic.Int64)), map[string]any{}) {
			_, rpcErr := w.call(t, "prompts/get", map[string]any{
				"name": "strict_prompt", "arguments": map[string]any{"size": "huge"},
			})
			if rpcErr == nil || rpcErr.Code != core.ErrCodeInvalidParams {
				t.Errorf("%s wire: prompts/get with an out-of-enum argument = %v, want -32602", w.name, rpcErr)
			}
		}
	})

	t.Run("1637 timeouts reach the handler", func(t *testing.T) {
		for _, tc := range []struct {
			name, method string
			opts         []Option
			params       map[string]any
		}{
			{"per-tool", "tools/call", nil, map[string]any{"name": "timed_tool"}},
			{"server-wide", "tools/call", []Option{WithToolTimeout(time.Minute)}, map[string]any{"name": "deadline_tool"}},
			{"per-prompt", "prompts/get", nil, map[string]any{"name": "timed_prompt"}},
			{"per-resource", "resources/read", nil, map[string]any{"uri": "test://timed"}},
			{"per-template", "resources/read", nil, map[string]any{"uri": "test://timed/1"}},
		} {
			for _, w := range parityWires(t, handlerParityFixture(t, new(atomic.Int64), tc.opts...), map[string]any{}) {
				res, rpcErr := w.call(t, tc.method, cloneParams(tc.params))
				if rpcErr != nil {
					t.Fatalf("%s, %s wire: %v", tc.name, w.name, rpcErr)
				}
				if raw, _ := json.Marshal(res); !strings.Contains(string(raw), "deadline=true") {
					t.Errorf("%s, %s wire: handler saw no deadline: %s", tc.name, w.name, raw)
				}
			}
		}
	})

	t.Run("1637 completion values are capped", func(t *testing.T) {
		for _, w := range parityWires(t, handlerParityFixture(t, new(atomic.Int64)), map[string]any{}) {
			res, rpcErr := w.call(t, "completion/complete", map[string]any{
				"ref":      map[string]any{"type": "ref/prompt", "name": "timed_prompt"},
				"argument": map[string]any{"name": "x", "value": ""},
			})
			if rpcErr != nil {
				t.Fatalf("%s wire: %v", w.name, rpcErr)
			}
			var c struct {
				Values  []string `json:"values"`
				Total   int      `json:"total"`
				HasMore bool     `json:"hasMore"`
			}
			_ = json.Unmarshal(res["completion"], &c)
			if len(c.Values) != core.MaxCompletionValues || c.Total != 2*core.MaxCompletionValues || !c.HasMore {
				t.Errorf("%s wire: %d values, total %d, hasMore %v; want %d, %d, true",
					w.name, len(c.Values), c.Total, c.HasMore, core.MaxCompletionValues, 2*core.MaxCompletionValues)
			}
		}
	})

	t.Run("1637 handler errors read the same", func(t *testing.T) {
		wires := parityWires(t, handlerParityFixture(t, new(atomic.Int64)), map[string]any{})
		for _, c := range []struct {
			method string
			params map[string]any
		}{
			{"prompts/get", map[string]any{"name": "boom_prompt"}},
			{"resources/read", map[string]any{"uri": "test://boom"}},
			{"resources/read", map[string]any{"uri": "test://boom/1"}},
			{"completion/complete", map[string]any{
				"ref":      map[string]any{"type": "ref/prompt", "name": "boom_prompt"},
				"argument": map[string]any{"name": "x", "value": ""},
			}},
		} {
			var got [2]string
			for i, w := range wires {
				_, rpcErr := w.call(t, c.method, cloneParams(c.params))
				if rpcErr == nil {
					t.Fatalf("%s %v, %s wire: no error", c.method, c.params, w.name)
				}
				got[i] = fmt.Sprintf("%d %s", rpcErr.Code, rpcErr.Message)
			}
			if got[0] != got[1] {
				t.Errorf("%s %v differs\n  session:   %s\n  stateless: %s", c.method, c.params, got[0], got[1])
			}
		}
	})

	t.Run("1637 stdio serves the stateless wire", func(t *testing.T) {
		s := parityFixture(t)
		var httpStateless parityWire
		for _, w := range parityWires(t, s, map[string]any{}) {
			if w.name == "stateless" {
				httpStateless = w
			}
		}
		stdio := stdioStatelessWire(t, s)
		for _, c := range parityCalls {
			want, rpcErr := httpStateless.call(t, c.method, cloneParams(c.params))
			if rpcErr != nil {
				t.Fatalf("http %s: %v", c.method, rpcErr)
			}
			got, rpcErr := stdio(t, c.method, cloneParams(c.params))
			if rpcErr != nil {
				t.Errorf("stdio %s: %v", c.method, rpcErr)
				continue
			}
			if g, w := canonicalParityResult(got), canonicalParityResult(want); g != w {
				t.Errorf("%s differs\n  stdio: %s\n  http:  %s", c.method, g, w)
			}
		}
	})

	t.Run("1625 middleware sees the handshake", func(t *testing.T) {
		var mu sync.Mutex
		var seen []string
		record := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
			mu.Lock()
			seen = append(seen, req.Method)
			mu.Unlock()
			return next(ctx, req)
		}
		parityWires(t, parityFixture(t, WithMiddleware(record)), map[string]any{})
		mu.Lock()
		defer mu.Unlock()
		for _, m := range []string{"initialize", "server/discover"} {
			if !slices.Contains(seen, m) {
				t.Errorf("middleware saw %v, want %s", seen, m)
			}
		}
	})

	t.Run("1625 metrics and trace see both wires", func(t *testing.T) {
		meter, tracer := newFakeMeter(), &fakeTracerProvider{}
		toolCalls := func() int {
			meter.mu.Lock()
			c := meter.counters[metricNameToolCalls]
			meter.mu.Unlock()
			if c == nil {
				return 0
			}
			return len(c.snapshot())
		}
		s := parityFixture(t, WithMeterProvider(meter), WithTracerProvider(tracer))
		for _, w := range parityWires(t, s, map[string]any{}) {
			calls, spans := toolCalls(), len(tracer.snapshot())
			if _, rpcErr := w.call(t, "tools/call", map[string]any{"name": "upload_report", "arguments": map[string]any{}}); rpcErr != nil {
				t.Fatalf("%s tools/call: %v", w.name, rpcErr)
			}
			if got := toolCalls(); got != calls+1 {
				t.Errorf("%s wire: %s went from %d to %d, want one more", w.name, metricNameToolCalls, calls, got)
			}
			if got := len(tracer.snapshot()); got <= spans {
				t.Errorf("%s wire: no span recorded for tools/call", w.name)
			}
		}
	})

	t.Run("1625 a panicking handler is an internal error", func(t *testing.T) {
		s := parityFixture(t)
		s.RegisterTool(core.ToolDef{Name: "panics"}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
			panic("boom")
		})
		for _, w := range parityWires(t, s, map[string]any{}) {
			_, rpcErr := w.call(t, "tools/call", map[string]any{"name": "panics", "arguments": map[string]any{}})
			if rpcErr == nil || rpcErr.Code != core.ErrCodeInternal {
				t.Errorf("%s wire: tools/call on a panicking tool = %v, want %d", w.name, rpcErr, core.ErrCodeInternal)
			}
		}
	})

	t.Run("1625 handlers get the same request context", func(t *testing.T) {
		relay := &countingRelay{}
		s := parityFixture(t, WithContentChunkMethod("x/chunk"), WithSubscriptions(), WithNotificationRelay(relay),
			WithAllowedRoots("/workspace"))
		s.RegisterTool(core.ToolDef{Name: "ctx_probe"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			core.NotifyResourceUpdated(ctx, "test://static")
			return core.TextResult(fmt.Sprintf("chunk=%s etcAllowed=%v",
				core.ContentChunkMethodFromContext(ctx), core.IsPathAllowed(ctx, "/etc/passwd"))), nil
		})
		for _, w := range parityWires(t, s, map[string]any{}) {
			before := relay.n.Load()
			res, rpcErr := w.call(t, "tools/call", map[string]any{"name": "ctx_probe", "arguments": map[string]any{}})
			if rpcErr != nil {
				t.Fatalf("%s tools/call: %v", w.name, rpcErr)
			}
			if !strings.Contains(string(res["content"]), "chunk=x/chunk") {
				t.Errorf("%s wire: handler saw no content chunk method: %s", w.name, res["content"])
			}
			if !strings.Contains(string(res["content"]), "etcAllowed=false") {
				t.Errorf("%s wire: WithAllowedRoots(/workspace) let /etc/passwd through: %s", w.name, res["content"])
			}
			if got := relay.n.Load(); got != before+1 {
				t.Errorf("%s wire: NotifyResourceUpdated published %d times, want 1", w.name, got-before)
			}
		}
	})

	t.Run("1625 notify interceptors see both wires", func(t *testing.T) {
		var seen atomic.Int64
		intercept := func(method string, params any, next core.NotifyFunc) {
			if method == "notifications/parity" {
				seen.Add(1)
			}
			next(method, params)
		}
		s := parityFixture(t, WithNotifyInterceptor(intercept))
		s.RegisterTool(core.ToolDef{Name: "notifies"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			ctx.Notify("notifications/parity", map[string]any{})
			return core.TextResult("ok"), nil
		})
		for _, w := range parityWires(t, s, map[string]any{}) {
			before := seen.Load()
			if _, rpcErr := w.call(t, "tools/call", map[string]any{"name": "notifies", "arguments": map[string]any{}}); rpcErr != nil {
				t.Fatalf("%s tools/call: %v", w.name, rpcErr)
			}
			if got := seen.Load() - before; got != 1 {
				t.Errorf("%s wire: interceptor saw the handler's notification %d times, want 1", w.name, got)
			}
		}
	})

	t.Run("1520 a notification gets no response on either wire", func(t *testing.T) {
		s := parityFixture(t)
		s.HandleMethod("notifications/parity_panics", func(core.MethodContext, json.RawMessage, json.RawMessage) *core.Response {
			panic("boom")
		})
		for _, w := range parityWires(t, s, map[string]any{}) {
			for _, method := range []string{"notifications/parity_unknown", "notifications/parity_panics"} {
				for _, nullID := range []bool{false, true} {
					if status, body := w.notify(t, method, nullID); status != http.StatusAccepted || strings.TrimSpace(body) != "" {
						t.Errorf("%s wire, %s (id null=%v): HTTP %d %q, want 202 and no body", w.name, method, nullID, status, body)
					}
				}
			}
			// SEP-2575 removed notifications/cancelled, and a removed method is
			// refused with 404; a notification still gets no JSON-RPC body.
			if w.name == "stateless" {
				if status, body := w.notify(t, "notifications/cancelled", false); status != http.StatusNotFound || strings.TrimSpace(body) != "" {
					t.Errorf("stateless wire, notifications/cancelled: HTTP %d %q, want 404 and no body", status, body)
				}
			}
		}
	})

	t.Run("1356 list methods page at 250 by default", func(t *testing.T) {
		lists := []struct{ method, key string }{
			{"tools/list", "tools"},
			{"resources/list", "resources"},
			{"resources/templates/list", "resourceTemplates"},
			{"prompts/list", "prompts"},
		}
		pageSizes := func(t *testing.T, w parityWire, method, key string) []int {
			var sizes []int
			cursor := ""
			for range 10 {
				params := map[string]any{}
				if cursor != "" {
					params["cursor"] = cursor
				}
				res, rpcErr := w.call(t, method, params)
				if rpcErr != nil {
					t.Fatalf("%s %s: %v", w.name, method, rpcErr)
				}
				var items []json.RawMessage
				_ = json.Unmarshal(res[key], &items)
				sizes = append(sizes, len(items))
				cursor = ""
				_ = json.Unmarshal(res["nextCursor"], &cursor)
				if cursor == "" {
					return sizes
				}
			}
			t.Fatalf("%s %s: no last page after 10 pages", w.name, method)
			return nil
		}

		for _, tc := range []struct {
			name string
			opts []Option
			want []int
		}{
			{"default", nil, []int{250, 1}},
			{"off", []Option{WithDefaultPageSize(0)}, []int{251}},
		} {
			s := parityFixture(t, tc.opts...)
			for i := range 250 {
				s.RegisterTool(core.ToolDef{Name: fmt.Sprintf("bulk_%03d", i)},
					func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
				s.RegisterResource(core.ResourceDef{URI: fmt.Sprintf("test://bulk/%03d", i), Name: "bulk"},
					func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) { return core.ResourceResult{}, nil })
				s.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: fmt.Sprintf("test://bulk/%03d/{id}", i), Name: "bulk"},
					func(core.ResourceContext, string, map[string]string) (core.ResourceResult, error) { return core.ResourceResult{}, nil })
				s.RegisterPrompt(core.PromptDef{Name: fmt.Sprintf("bulk_%03d", i)},
					func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) { return core.PromptResult{}, nil })
			}
			for _, w := range parityWires(t, s, map[string]any{}) {
				for _, l := range lists {
					if got := pageSizes(t, w, l.method, l.key); !slices.Equal(got, tc.want) {
						t.Errorf("%s %s wire %s: page sizes %v, want %v", tc.name, w.name, l.method, got, tc.want)
					}
				}
			}
		}
	})

	t.Run("1356 an invalid cursor is invalid params", func(t *testing.T) {
		for _, w := range parityWires(t, parityFixture(t), map[string]any{}) {
			for _, method := range []string{"tools/list", "resources/list", "resources/templates/list", "prompts/list"} {
				_, rpcErr := w.call(t, method, map[string]any{"cursor": "not-a-cursor"})
				if rpcErr == nil || rpcErr.Code != core.ErrCodeInvalidParams {
					t.Errorf("%s wire %s: got %+v, want %d", w.name, method, rpcErr, core.ErrCodeInvalidParams)
				}
			}
		}
	})

	t.Run("1111 Mcp-Param headers match the arguments they mirror", func(t *testing.T) {
		s := parityFixture(t)
		s.RegisterTool(core.ToolDef{
			Name: "route_order",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"region":   map[string]any{"type": "string", "x-mcp-header": "Region"},
					"priority": map[string]any{"type": "integer", "x-mcp-header": "Priority"},
					"urgent":   map[string]any{"type": "boolean", "x-mcp-header": "Urgent"},
					"note":     map[string]any{"type": "string"},
				},
				"required": []any{"region"},
			},
		}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })

		for _, tc := range []struct {
			name    string
			args    map[string]any
			headers map[string]string
			accept  bool
		}{
			{"match", map[string]any{"region": "us-east", "priority": 5}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Priority": "5"}, true},
			{"string mismatch", map[string]any{"region": "us-east"}, map[string]string{"Mcp-Param-Region": "eu-west"}, false},
			{"header missing", map[string]any{"region": "us-east"}, nil, false},
			{"base64", map[string]any{"region": "Hello"}, map[string]string{"Mcp-Param-Region": "=?base64?SGVsbG8=?="}, true},
			{"base64 bad padding", map[string]any{"region": "Hello"}, map[string]string{"Mcp-Param-Region": "=?base64?SGVsbG8?="}, false},
			{"base64 bad chars", map[string]any{"region": "Hello"}, map[string]string{"Mcp-Param-Region": "=?base64?SGVs!!!bG8=?="}, false},
			{"no prefix is literal", map[string]any{"region": "SGVsbG8="}, map[string]string{"Mcp-Param-Region": "SGVsbG8="}, true},
			{"no suffix is literal", map[string]any{"region": "=?base64?SGVsbG8="}, map[string]string{"Mcp-Param-Region": "=?base64?SGVsbG8="}, true},
			{"unencoded non-ascii", map[string]any{"region": "café"}, map[string]string{"Mcp-Param-Region": "café"}, false},
			{"optional omitted", map[string]any{"region": "us-east"}, map[string]string{"Mcp-Param-Region": "us-east"}, true},
			{"optional null", map[string]any{"region": "us-east", "priority": nil}, map[string]string{"Mcp-Param-Region": "us-east"}, true},
			{"header without body value", map[string]any{"region": "us-east"}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Priority": "5"}, false},
			{"number compared numerically", map[string]any{"region": "us-east", "priority": 5}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Priority": "5.0"}, true},
			{"number mismatch", map[string]any{"region": "us-east", "priority": 5}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Priority": "6"}, false},
			{"boolean", map[string]any{"region": "us-east", "urgent": true}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Urgent": "true"}, true},
			{"boolean mismatch", map[string]any{"region": "us-east", "urgent": true}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Urgent": "yes"}, false},
			{"required omitted", map[string]any{"note": "n"}, nil, false},
			{"unannotated header ignored", map[string]any{"region": "us-east"}, map[string]string{"Mcp-Param-Region": "us-east", "Mcp-Param-Other": "x"}, true},
		} {
			for _, w := range parityWires(t, s, map[string]any{}) {
				status, _, rpcErr := w.callHeaders(t, "tools/call", map[string]any{"name": "route_order", "arguments": tc.args}, tc.headers)
				switch {
				case tc.accept && rpcErr != nil:
					t.Errorf("%s %s wire: refused with %+v", tc.name, w.name, rpcErr)
				case !tc.accept && (rpcErr == nil || rpcErr.Code != core.ErrCodeHeaderMismatch || status != http.StatusBadRequest):
					t.Errorf("%s %s wire: got HTTP %d and %+v, want 400 and %d", tc.name, w.name, status, rpcErr, core.ErrCodeHeaderMismatch)
				}
			}
		}
	})

	t.Run("same result on both wires", func(t *testing.T) {
		wires := parityWires(t, parityFixture(t), map[string]any{})
		for _, c := range parityCalls {
			var got [2]string
			for i, w := range wires {
				res, rpcErr := w.call(t, c.method, cloneParams(c.params))
				if rpcErr != nil {
					t.Fatalf("%s %s: %v", w.name, c.method, rpcErr)
				}
				got[i] = canonicalParityResult(res)
			}
			if got[0] != got[1] {
				t.Errorf("%s differs\n  session:   %s\n  stateless: %s", c.method, got[0], got[1])
			}
		}
	})
}

// knownWireDifferences are the result keys that legitimately differ between
// the wires. Each entry says why. Anything else that differs is a parity bug.
var knownWireDifferences = map[string]string{
	// The stateless wire has no initialize to carry server identity, so it
	// stamps _meta[io.modelcontextprotocol/serverInfo] on every result.
	"_meta": "stateless serverInfo stamp (spec PR 3002)",
}

func canonicalParityResult(res map[string]json.RawMessage) string {
	keys := make([]string, 0, len(res))
	for k := range res {
		if _, known := knownWireDifferences[k]; !known {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s ", k, res[k])
	}
	return b.String()
}

// handlerParityFixture adds what the 1637 rows call to parityFixture. Every
// timed handler answers "deadline=<bool>" so a row can see whether its
// timeout reached the handler's context. runs counts calls to "strict".
func handlerParityFixture(t *testing.T, runs *atomic.Int64, opts ...Option) *Server {
	t.Helper()
	s := parityFixture(t, opts...)
	deadline := func(ctx context.Context) string {
		_, ok := ctx.Deadline()
		return fmt.Sprintf("deadline=%v", ok)
	}
	boom := errors.New("boom")

	s.RegisterTool(core.ToolDef{
		Name: "strict",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"design": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
	}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		runs.Add(1)
		return core.TextResult("ran"), nil
	})
	for _, def := range []core.ToolDef{
		{Name: "timed_tool", Timeout: time.Minute},
		{Name: "deadline_tool"},
	} {
		s.RegisterTool(def, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult(deadline(ctx)), nil
		})
	}

	s.RegisterPrompt(core.PromptDef{
		Name:      "strict_prompt",
		Arguments: []core.PromptArgument{{Name: "size", Schema: map[string]any{"type": "string", "enum": []any{"small", "large"}}}},
	}, func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
		return core.PromptResult{}, nil
	})
	s.RegisterPrompt(core.PromptDef{Name: "timed_prompt", Timeout: time.Minute},
		func(ctx core.PromptContext, _ core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{Messages: []core.PromptMessage{{Role: "user", Content: core.Content{Type: "text", Text: deadline(ctx)}}}}, nil
		})
	s.RegisterPrompt(core.PromptDef{Name: "boom_prompt"},
		func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) { return nil, boom })

	s.RegisterResource(core.ResourceDef{URI: "test://timed", Name: "timed", Timeout: time.Minute},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: req.URI, Text: deadline(ctx)}}}, nil
		})
	s.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: "test://timed/{id}", Name: "timed_tmpl", Timeout: time.Minute},
		func(ctx core.ResourceContext, uri string, _ map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: uri, Text: deadline(ctx)}}}, nil
		})
	s.RegisterResource(core.ResourceDef{URI: "test://boom", Name: "boom"},
		func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, boom
		})
	s.RegisterResourceTemplate(core.ResourceTemplate{URITemplate: "test://boom/{id}", Name: "boom_tmpl"},
		func(core.ResourceContext, string, map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{}, boom
		})

	many := make([]string, 2*core.MaxCompletionValues)
	for i := range many {
		many[i] = fmt.Sprintf("v%d", i)
	}
	s.RegisterCompletion("ref/prompt", "timed_prompt",
		func(core.PromptContext, core.CompletionRef, core.CompletionArgument) (core.CompletionResult, error) {
			return core.CompletionResult{Values: many}, nil
		})
	s.RegisterCompletion("ref/prompt", "boom_prompt",
		func(core.PromptContext, core.CompletionRef, core.CompletionArgument) (core.CompletionResult, error) {
			return core.CompletionResult{}, boom
		})
	return s
}

// stdioStatelessWire serves s over an io.Pipe pair and returns a call func
// that sends each request with the 2026-07-28 _meta envelope and no
// initialize, as a stateless client would over stdio.
func stdioStatelessWire(t *testing.T, s *Server) func(t *testing.T, method string, params map[string]any) (map[string]json.RawMessage, *core.Error) {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = s.RunIO(ctx, sr, sw)
		sw.Close()
	}()
	t.Cleanup(func() {
		cancel()
		cw.Close()
	})
	lines := bufio.NewScanner(cr)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	var nextID int
	return func(t *testing.T, method string, params map[string]any) (map[string]json.RawMessage, *core.Error) {
		t.Helper()
		if params == nil {
			params = map[string]any{}
		}
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "parity", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
		nextID++
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": method, "params": params})
		if _, err := cw.Write(append(frame, '\n')); err != nil {
			t.Fatalf("stdio write: %v", err)
		}
		if !lines.Scan() {
			t.Fatalf("stdio %s: no response (%v)", method, lines.Err())
		}
		var resp struct {
			Result map[string]any `json:"result"`
			Error  *core.Error    `json:"error"`
		}
		if err := json.Unmarshal(lines.Bytes(), &resp); err != nil {
			t.Fatalf("stdio %s: %v: %s", method, err, lines.Bytes())
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		// Re-marshal through any, as parityWires does, so key order matches.
		out := make(map[string]json.RawMessage, len(resp.Result))
		for k, v := range resp.Result {
			out[k], _ = json.Marshal(v)
		}
		return out, nil
	}
}

// countingRelay counts what the subscription registry publishes, which is
// how a test sees core.NotifyResourceUpdated reach it.
type countingRelay struct{ n atomic.Int64 }

func (r *countingRelay) Publish(context.Context, string, any) { r.n.Add(1) }

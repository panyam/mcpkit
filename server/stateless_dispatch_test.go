package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// statelessReq builds a stateless-wire request for method with params, which
// carry the minimum valid _meta envelope unless the caller passes its own.
func statelessReq(t *testing.T, method string, params any) *core.Request {
	t.Helper()
	if params == nil {
		params = validMetaParams()
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return &core.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: core.NewRawJSON(raw)}
}

func TestDispatchStateless_RemovedMethodReturns32601(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
	for _, method := range []string{
		"initialize", "notifications/initialized", "ping",
		"logging/setLevel", "resources/subscribe", "resources/unsubscribe",
	} {
		t.Run(method, func(t *testing.T) {
			resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, method, nil))
			if resp == nil || resp.Error == nil {
				t.Fatalf("expected error response for removed method %q", method)
			}
			if resp.Error.Code != core.ErrCodeMethodNotFound {
				t.Errorf("method %q got code %d, want %d (-32601)", method, resp.Error.Code, core.ErrCodeMethodNotFound)
			}
			if got := stateless.HTTPStatusForCode(resp.Error.Code); got != 404 {
				t.Errorf("HTTPStatusForCode(-32601) = %d, want 404", got)
			}
		})
	}
}

func TestDispatchStateless_MissingMetaReturns32602(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
	resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, "tools/list", map[string]any{}))
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error response for missing _meta")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("got code %d, want %d (-32602)", resp.Error.Code, core.ErrCodeInvalidParams)
	}
	if got := stateless.HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32602) = %d, want 400", got)
	}
}

func TestDispatchStateless_UnsupportedVersionReturns32022(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
	resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, "tools/list", map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "1900-01-01",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "c", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}))
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error response for unsupported version")
	}
	// Pin the literal wire value, not just the constant: the SEP-2575 draft
	// schema fixes this at -32022, and the upstream server conformance
	// scenario does NOT assert the code (only HTTP 400 + the data members),
	// so without a literal check a drift here would pass conformance.
	if resp.Error.Code != -32022 {
		t.Errorf("got code %d, want -32022 (SEP-2575 UnsupportedProtocolVersion)", resp.Error.Code)
	}
	if resp.Error.Code != core.ErrCodeUnsupportedProtocolVersion {
		t.Errorf("constant ErrCodeUnsupportedProtocolVersion = %d, drifted from wire value -32022",
			core.ErrCodeUnsupportedProtocolVersion)
	}
	if got := stateless.HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32022) = %d, want 400", got)
	}
	raw, _ := json.Marshal(resp.Error.Data)
	var data core.UnsupportedProtocolVersionData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("error data did not decode as UnsupportedProtocolVersionData: %v", err)
	}
	if len(data.Supported) == 0 {
		t.Errorf("data.Supported empty; want at least one version")
	}
	if data.Requested != "1900-01-01" {
		t.Errorf("data.Requested = %q, want %q", data.Requested, "1900-01-01")
	}
}

func TestDispatchStateless_ServerDiscoverShape(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "0.0.1"})
	s.RegisterTool(core.ToolDef{Name: "t"}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})
	resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, "server/discover", nil))
	if resp == nil || resp.Error != nil {
		t.Fatalf("server/discover failed: %+v", resp)
	}
	raw, _ := json.Marshal(resp.Result)
	var result struct {
		stateless.DiscoverResult
		Meta       core.ResultMeta `json:"_meta"`
		ResultType string          `json:"resultType"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("discover result did not decode: %v", err)
	}
	if len(result.SupportedVersions) == 0 {
		t.Errorf("SupportedVersions empty")
	}
	// Spec PR 3002: server identity lives in the result _meta, not the body.
	if result.Meta.ServerInfo == nil || result.Meta.ServerInfo.Name != "test" {
		t.Errorf("_meta serverInfo = %+v, want name %q", result.Meta.ServerInfo, "test")
	}
	if result.Capabilities.Tools == nil || !result.Capabilities.Tools.ListChanged {
		t.Errorf("Capabilities not echoed correctly: %+v", result.Capabilities)
	}
	// 2026-07-28 final revision: resultType on every result, and the SEP-2549
	// caching hints required on DiscoverResult.
	if result.ResultType != "complete" {
		t.Errorf("resultType = %q, want complete", result.ResultType)
	}
	var hints map[string]any
	json.Unmarshal(raw, &hints)
	if _, ok := hints["ttlMs"]; !ok {
		t.Error("ttlMs missing from DiscoverResult")
	}
	if hints["cacheScope"] != core.CacheScopePublic {
		t.Errorf("cacheScope = %v, want public", hints["cacheScope"])
	}
}

// A request whose _meta omits clientInfo MUST be served, not rejected with
// -32602 (spec PR 3002; conformance check
// sep-2575-request-meta-client-info-optional).
func TestDispatchStateless_ClientInfoOptional(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
	resp, err := s.dispatchStateless(context.Background(), statelessReq(t, "tools/list", map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}))
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || resp.Error != nil {
		t.Fatalf("clientInfo-less request rejected: %+v", resp)
	}
}

// Every success result, not just server/discover, carries the server identity
// in _meta and the resultType discriminator (spec PR 3002, 2026-07-28).
func TestDispatchStateless_StampsEveryResult(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "2.0"})
	for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
		t.Run(method, func(t *testing.T) {
			resp, err := s.dispatchStateless(context.Background(), statelessReq(t, method, nil))
			if err != nil || resp == nil || resp.Error != nil {
				t.Fatalf("dispatch failed: resp=%+v err=%v", resp, err)
			}
			raw, _ := core.MarshalJSON(resp.Result)
			var probe struct {
				Meta       core.ResultMeta `json:"_meta"`
				ResultType string          `json:"resultType"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("result did not decode: %v", err)
			}
			if probe.Meta.ServerInfo == nil || probe.Meta.ServerInfo.Name != "test" || probe.Meta.ServerInfo.Version != "2.0" {
				t.Errorf("_meta serverInfo = %+v, want test/2.0 (result=%s)", probe.Meta.ServerInfo, raw)
			}
			if probe.ResultType != "complete" {
				t.Errorf("resultType = %q, want complete", probe.ResultType)
			}
		})
	}
}

func TestDispatchStateless_UnknownMethodReturns32601(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
	resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, "frobnicate", nil))
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeMethodNotFound {
		t.Fatalf("got %+v, want -32601 method-not-found", resp)
	}
	if got := stateless.HTTPStatusForCode(resp.Error.Code); got != 404 {
		t.Errorf("HTTPStatusForCode(-32601) = %d, want 404", got)
	}
}

// Past the envelope checks there is one route into the middleware chain
// (#1625), so server/discover and an unknown method reach it exactly like
// tools/list does, and a removed method or a bad envelope never does.
func TestDispatchStateless_EveryMethodGoesThroughTheChain(t *testing.T) {
	var seen []string
	record := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		seen = append(seen, req.Method)
		return next(ctx, req)
	}
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"}, WithMiddleware(record))
	for _, tc := range []struct {
		method string
		params any
		want   bool
	}{
		{"server/discover", nil, true},
		{"tools/list", nil, true},
		{"frobnicate", nil, true},
		{"initialize", nil, false},
		{"tools/list", map[string]any{}, false},
	} {
		seen = nil
		s.dispatchStateless(context.Background(), statelessReq(t, tc.method, tc.params))
		if got := slices.Equal(seen, []string{tc.method}); got != tc.want {
			t.Errorf("%s with params %v: middleware saw %v, want reached=%v", tc.method, tc.params, seen, tc.want)
		}
	}
}

// A middleware short-circuit must come out of dispatchStateless as the error,
// unfolded and with a nil response, for a built-in method and a custom one
// alike. The transport relies on it to send 403 + WWW-Authenticate via
// writeAuthError (issue 815); folded into a -32603 body it would go out as
// HTTP 200 and lose the scope challenge.
func TestDispatchStateless_MiddlewareAuthErrorPropagates(t *testing.T) {
	authErr := &core.AuthError{
		Code:            http.StatusForbidden,
		Message:         "insufficient scope",
		WWWAuthenticate: `Bearer error="insufficient_scope", scope="docs:write"`,
	}
	deny := func(context.Context, *core.Request, MiddlewareFunc) (*core.Response, error) { return nil, authErr }
	s := NewServer(core.ServerInfo{Name: "test", Version: "1"}, WithMiddleware(deny))
	for _, method := range []string{"tools/call", "events/poll"} {
		resp, err := s.dispatchStateless(context.Background(), statelessReq(t, method, nil))
		var got *core.AuthError
		if !errors.As(err, &got) {
			t.Fatalf("%s: err = %T (%v), want *core.AuthError", method, err, err)
		}
		if got.Code != http.StatusForbidden || got.WWWAuthenticate != authErr.WWWAuthenticate {
			t.Errorf("%s: AuthError = %+v, want %+v", method, got, authErr)
		}
		if resp != nil {
			t.Errorf("%s: resp = %+v, want nil on the error path", method, resp)
		}
	}
}

// Handlers read the per-request _meta envelope from ctx, logLevel included:
// SEP-2575 says a stateless server MUST NOT log during a tools/call unless the
// client opted in there. The send-or-not behavior is graded end to end by the
// upstream ServerNoLogWithoutLogLevel check in testconf-stateless.
func TestDispatchStateless_HandlerSeesLogLevel(t *testing.T) {
	for _, lvl := range []string{"", "info", "debug"} {
		var captured *core.RequestMeta
		s := NewServer(core.ServerInfo{Name: "test", Version: "1"})
		s.RegisterTool(core.ToolDef{Name: "spy_log_level"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			captured = stateless.RequestMetaFromContext(ctx)
			return core.TextResult("ok"), nil
		})
		meta := map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "c", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
		if lvl != "" {
			meta["io.modelcontextprotocol/logLevel"] = lvl
		}
		resp, _ := s.dispatchStateless(context.Background(), statelessReq(t, "tools/call", map[string]any{
			"name": "spy_log_level", "arguments": map[string]any{}, "_meta": meta,
		}))
		if resp == nil || resp.Error != nil {
			t.Fatalf("logLevel %q: tools/call errored: %+v", lvl, resp)
		}
		if captured == nil {
			t.Fatalf("logLevel %q: handler did not see a RequestMeta in ctx", lvl)
		}
		if captured.LogLevel != lvl {
			t.Errorf("captured.LogLevel = %q, want %q", captured.LogLevel, lvl)
		}
	}
}

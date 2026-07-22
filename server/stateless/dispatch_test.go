package stateless

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// fakeBackend is a minimal Backend for dispatcher tests. Each field is
// optional; nil functions return empty values.
type fakeBackend struct {
	info      core.ServerInfo
	caps      core.ServerCapabilities
	versions  []string
	tools     []core.ToolDef
	tool      func(name string) (core.ToolDef, core.ToolHandler, bool)
	resource  func(uri string) (core.ResourceDef, core.ResourceHandler, bool)
	ttlMs     *int
	scope     string
	readTTL   *int
	readScope string
	authErr   error // when non-nil, InvokeWithMiddleware short-circuits with it
}

func (f *fakeBackend) ServerInfo() core.ServerInfo           { return f.info }
func (f *fakeBackend) Capabilities() core.ServerCapabilities { return f.caps }
func (f *fakeBackend) SupportedVersions() []string {
	if f.versions == nil {
		return []string{core.DraftProtocolVersion2026V1}
	}
	return f.versions
}
func (f *fakeBackend) Tools() []core.ToolDef { return f.tools }
func (f *fakeBackend) Tool(name string) (core.ToolDef, core.ToolHandler, bool) {
	if f.tool != nil {
		return f.tool(name)
	}
	return core.ToolDef{}, nil, false
}
func (f *fakeBackend) Resources() []core.ResourceDef { return nil }
func (f *fakeBackend) Resource(uri string) (core.ResourceDef, core.ResourceHandler, bool) {
	if f.resource != nil {
		return f.resource(uri)
	}
	return core.ResourceDef{}, nil, false
}
func (f *fakeBackend) ResourceTemplates() []core.ResourceTemplate { return nil }
func (f *fakeBackend) ResourceTemplate(string) (core.ResourceTemplate, core.TemplateHandler, bool) {
	return core.ResourceTemplate{}, nil, false
}
func (f *fakeBackend) Prompts() []core.PromptDef { return nil }
func (f *fakeBackend) Prompt(string) (core.PromptDef, core.PromptHandler, bool) {
	return core.PromptDef{}, nil, false
}
func (f *fakeBackend) Completion(string, string) (core.CompletionHandler, bool) { return nil, false }
func (f *fakeBackend) ListTTLMs() *int                                          { return f.ttlMs }
func (f *fakeBackend) ListCacheScope() string                                   { return f.scope }
func (f *fakeBackend) ReadTTLMs() *int                                          { return f.readTTL }
func (f *fakeBackend) ReadCacheScope() string                                   { return f.readScope }

// InvokeWithMiddleware returns (nil, nil, false) so the dispatcher falls back
// to its built-in per-method handler. The fake doesn't model server-level
// middleware or custom-method registrations — both belong to production
// statelessBackend (server package). The authErr field, when set, models a
// middleware short-circuit so dispatch_autherror_test.go can assert the error
// propagates out of Dispatch unfolded.
func (f *fakeBackend) InvokeWithMiddleware(context.Context, *core.Request) (*core.Response, error, bool) {
	if f.authErr != nil {
		return nil, f.authErr, true
	}
	return nil, nil, false
}

// validMetaParams is a params blob with the minimum valid _meta envelope.
func validMetaParams(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": {"name": "test-client", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": {}
		}
	}`)
}

func TestDispatch_RemovedMethodReturns32601(t *testing.T) {
	d := New(&fakeBackend{})
	cases := []string{
		"initialize",
		"notifications/initialized",
		"ping",
		"logging/setLevel",
		"resources/subscribe",
		"resources/unsubscribe",
	}
	for _, method := range cases {
		t.Run(method, func(t *testing.T) {
			req := &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage("1"),
				Method:  method,
				Params:  core.NewRawJSON(validMetaParams(t)),
			}
			resp, _ := d.Dispatch(context.Background(), req)
			if resp == nil || resp.Error == nil {
				t.Fatalf("expected error response for removed method %q", method)
			}
			if resp.Error.Code != core.ErrCodeMethodNotFound {
				t.Errorf("method %q got code %d, want %d (-32601)",
					method, resp.Error.Code, core.ErrCodeMethodNotFound)
			}
			if got := HTTPStatusForCode(resp.Error.Code); got != 404 {
				t.Errorf("HTTPStatusForCode(-32601) = %d, want 404", got)
			}
		})
	}
}

func TestDispatch_MissingMetaReturns32602(t *testing.T) {
	d := New(&fakeBackend{})
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "tools/list",
		Params:  core.NewRawJSON(json.RawMessage(`{}`)),
	}
	resp, _ := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error response for missing _meta")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("got code %d, want %d (-32602)",
			resp.Error.Code, core.ErrCodeInvalidParams)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32602) = %d, want 400", got)
	}
}

func TestDispatch_UnsupportedVersionReturns32022(t *testing.T) {
	d := New(&fakeBackend{versions: []string{core.DraftProtocolVersion2026V1}})
	bad := json.RawMessage(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "1900-01-01",
			"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": {}
		}
	}`)
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("3"),
		Method:  "tools/list",
		Params:  core.NewRawJSON(bad),
	}
	resp, _ := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error response for unsupported version")
	}
	// Pin the literal wire value, not just the constant: the SEP-2575 draft
	// schema fixes this at -32022, and the upstream server conformance
	// scenario does NOT assert the code (only HTTP 400 + the data members),
	// so without a literal check a drift here would pass conformance.
	if resp.Error.Code != -32022 {
		t.Errorf("got code %d, want -32022 (SEP-2575 UnsupportedProtocolVersion)",
			resp.Error.Code)
	}
	if resp.Error.Code != core.ErrCodeUnsupportedProtocolVersion {
		t.Errorf("constant ErrCodeUnsupportedProtocolVersion = %d, drifted from wire value -32022",
			core.ErrCodeUnsupportedProtocolVersion)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32022) = %d, want 400", got)
	}

	// Payload shape MUST carry supported + requested per the draft schema.
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

// TestDispatch_ResourcesReadAppliesReadCacheDefaults pins the legacy↔stateless
// parity for SEP-2549: a server configured with WithReadResourceCacheControl
// must emit the same ttlMs / cacheScope hints on resources/read over the
// stateless wire as it does on the legacy wire, and a handler-set value must
// win over the default.
func TestDispatch_ResourcesReadAppliesReadCacheDefaults(t *testing.T) {
	ttl := 4200
	resWith := func(r core.ResourceResult) func(string) (core.ResourceDef, core.ResourceHandler, bool) {
		return func(string) (core.ResourceDef, core.ResourceHandler, bool) {
			return core.ResourceDef{URI: "file://x"}, func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) {
				return r, nil
			}, true
		}
	}
	readReq := func() *core.Request {
		return &core.Request{
			JSONRPC: "2.0",
			ID:      json.RawMessage("9"),
			Method:  "resources/read",
			Params: core.NewRawJSON(json.RawMessage(`{
				"uri": "file://x",
				"_meta": {
					"io.modelcontextprotocol/protocolVersion": "2026-07-28",
					"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
					"io.modelcontextprotocol/clientCapabilities": {}
				}
			}`)),
		}
	}
	decode := func(t *testing.T, resp *core.Response) core.ResourceResult {
		t.Helper()
		if resp == nil || resp.Error != nil {
			t.Fatalf("resources/read failed: %+v", resp)
		}
		raw, _ := json.Marshal(resp.Result)
		var got core.ResourceResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		return got
	}

	t.Run("default applied when handler leaves hints unset", func(t *testing.T) {
		d := New(&fakeBackend{
			readTTL:   &ttl,
			readScope: "private",
			resource:  resWith(core.ResourceResult{}),
		})
		resp, _ := d.Dispatch(context.Background(), readReq())
		got := decode(t, resp)
		if got.TTLMs == nil || *got.TTLMs != ttl {
			t.Errorf("TTLMs = %v, want %d (read default)", got.TTLMs, ttl)
		}
		if got.CacheScope != "private" {
			t.Errorf("CacheScope = %q, want private (read default)", got.CacheScope)
		}
	})

	t.Run("handler value overrides the default", func(t *testing.T) {
		handlerTTL := 99
		d := New(&fakeBackend{
			readTTL:   &ttl,
			readScope: "private",
			resource:  resWith(core.ResourceResult{TTLMs: &handlerTTL, CacheScope: "public"}),
		})
		resp, _ := d.Dispatch(context.Background(), readReq())
		got := decode(t, resp)
		if got.TTLMs == nil || *got.TTLMs != handlerTTL {
			t.Errorf("TTLMs = %v, want %d (handler override)", got.TTLMs, handlerTTL)
		}
		if got.CacheScope != "public" {
			t.Errorf("CacheScope = %q, want public (handler override)", got.CacheScope)
		}
	})
}

func TestDispatch_ServerDiscoverShape(t *testing.T) {
	caps := core.ServerCapabilities{Tools: &core.ToolsCap{ListChanged: true}}
	info := core.ServerInfo{Name: "test", Version: "0.0.1"}
	d := New(&fakeBackend{info: info, caps: caps})

	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("4"),
		Method:  "server/discover",
		Params:  core.NewRawJSON(validMetaParams(t)),
	}
	resp, _ := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("server/discover failed: %+v", resp)
	}

	raw, _ := json.Marshal(resp.Result)
	var result struct {
		DiscoverResult
		Meta core.ResultMeta `json:"_meta"`
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
}

// TestDispatch_ClientInfoOptional locks the spec PR 3002 demotion at the
// dispatcher level: a request whose _meta omits clientInfo MUST be served,
// not rejected with -32602 (conformance check
// sep-2575-request-meta-client-info-optional).
func TestDispatch_ClientInfoOptional(t *testing.T) {
	d := New(&fakeBackend{info: core.ServerInfo{Name: "test", Version: "1"}})
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("7"),
		Method:  "tools/list",
		Params: core.NewRawJSON(json.RawMessage(`{
			"_meta": {
				"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": {}
			}
		}`)),
	}
	resp, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if resp == nil || resp.Error != nil {
		t.Fatalf("clientInfo-less request rejected: %+v", resp)
	}
}

// TestDispatch_StampsServerInfoOnEveryResult verifies the spec PR 3002
// SHOULD: every success result — not just server/discover — carries the
// server identity in _meta[io.modelcontextprotocol/serverInfo].
func TestDispatch_StampsServerInfoOnEveryResult(t *testing.T) {
	d := New(&fakeBackend{info: core.ServerInfo{Name: "test", Version: "2.0"}})
	for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
		t.Run(method, func(t *testing.T) {
			req := &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage("8"),
				Method:  method,
				Params:  core.NewRawJSON(validMetaParams(t)),
			}
			resp, err := d.Dispatch(context.Background(), req)
			if err != nil || resp == nil || resp.Error != nil {
				t.Fatalf("dispatch failed: resp=%+v err=%v", resp, err)
			}
			raw, _ := core.MarshalJSON(resp.Result)
			var probe struct {
				Meta core.ResultMeta `json:"_meta"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("result did not decode: %v", err)
			}
			if probe.Meta.ServerInfo == nil || probe.Meta.ServerInfo.Name != "test" || probe.Meta.ServerInfo.Version != "2.0" {
				t.Errorf("_meta serverInfo = %+v, want test/2.0 (result=%s)", probe.Meta.ServerInfo, raw)
			}
		})
	}
}

func TestDispatch_ToolsCallMissingCapReturns32003(t *testing.T) {
	// The dispatcher should translate a typed *core.MissingCapabilityError
	// from a tool handler into a JSON-RPC -32021 response with the
	// required-cap payload shape. Locked here so future refactors of
	// translateToolError surface in this test, not the conformance audit.
	required := core.ClientCapabilities{Sampling: &struct{}{}}
	h := func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.ToolResult{}, &core.MissingCapabilityError{
			Required: required,
			Message:  "this tool requires sampling",
		}
	}
	d := New(&fakeBackend{
		tool: func(name string) (core.ToolDef, core.ToolHandler, bool) {
			if name == "test_missing_capability" {
				return core.ToolDef{Name: name}, h, true
			}
			return core.ToolDef{}, nil, false
		},
	})

	params := json.RawMessage(`{
		"name": "test_missing_capability",
		"arguments": {},
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": {}
		}
	}`)
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("5"),
		Method:  "tools/call",
		Params:  core.NewRawJSON(params),
	}
	resp, _ := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatal("expected -32021 for missing-capability tool")
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Errorf("got code %d, want %d (-32021)",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32021) = %d, want 400", got)
	}

	raw, _ := json.Marshal(resp.Error.Data)
	var data core.MissingRequiredClientCapabilityData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("data did not decode: %v", err)
	}
	if data.RequiredCapabilities.Sampling == nil {
		t.Errorf("RequiredCapabilities.Sampling missing; got %+v", data.RequiredCapabilities)
	}
}

func TestDispatch_UnknownMethodReturns32601(t *testing.T) {
	d := New(&fakeBackend{})
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("6"),
		Method:  "frobnicate",
		Params:  core.NewRawJSON(validMetaParams(t)),
	}
	resp, _ := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil || resp.Error.Code != core.ErrCodeMethodNotFound {
		t.Fatalf("got %+v, want -32601 method-not-found", resp)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 404 {
		t.Errorf("HTTPStatusForCode(-32601) = %d, want 404", got)
	}
}

func TestRequestMetaFromContext(t *testing.T) {
	if got := RequestMetaFromContext(context.Background()); got != nil {
		t.Errorf("RequestMetaFromContext(bare) = %+v, want nil", got)
	}
	meta := &core.RequestMeta{ProtocolVersion: "2026-07-28"}
	ctx := core.WithRequestMeta(context.Background(), meta)
	if got := RequestMetaFromContext(ctx); got != meta {
		t.Errorf("RequestMetaFromContext: got %+v, want %+v", got, meta)
	}
}

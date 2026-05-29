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
	info     core.ServerInfo
	caps     core.ServerCapabilities
	versions []string
	tools    []core.ToolDef
	tool     func(name string) (core.ToolDef, core.ToolHandler, bool)
	ttlMs    *int
	scope    string
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
func (f *fakeBackend) Resource(string) (core.ResourceDef, core.ResourceHandler, bool) {
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

// InvokeWithMiddleware returns (nil, false) so the dispatcher falls back
// to its built-in per-method handler. The fake doesn't model server-level
// middleware or custom-method registrations — both belong to production
// statelessBackend (server package).
func (f *fakeBackend) InvokeWithMiddleware(context.Context, *core.Request) (*core.Response, bool) {
	return nil, false
}

// validMetaParams is a params blob with the minimum valid _meta envelope.
func validMetaParams(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "DRAFT-2026-v1",
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
				Params:  validMetaParams(t),
			}
			resp := d.Dispatch(context.Background(), req)
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
		Params:  json.RawMessage(`{}`),
	}
	resp := d.Dispatch(context.Background(), req)
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

func TestDispatch_UnsupportedVersionReturns32004(t *testing.T) {
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
		Params:  bad,
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatal("expected error response for unsupported version")
	}
	if resp.Error.Code != core.ErrCodeUnsupportedProtocolVersion {
		t.Errorf("got code %d, want %d (-32004)",
			resp.Error.Code, core.ErrCodeUnsupportedProtocolVersion)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32004) = %d, want 400", got)
	}

	// Payload shape MUST carry supported + requested per upstream schema.
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

func TestDispatch_ServerDiscoverShape(t *testing.T) {
	caps := core.ServerCapabilities{Tools: &core.ToolsCap{ListChanged: true}}
	info := core.ServerInfo{Name: "test", Version: "0.0.1"}
	d := New(&fakeBackend{info: info, caps: caps})

	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("4"),
		Method:  "server/discover",
		Params:  validMetaParams(t),
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error != nil {
		t.Fatalf("server/discover failed: %+v", resp)
	}

	raw, _ := json.Marshal(resp.Result)
	var result DiscoverResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("discover result did not decode: %v", err)
	}
	if len(result.SupportedVersions) == 0 {
		t.Errorf("SupportedVersions empty")
	}
	if result.ServerInfo.Name != "test" {
		t.Errorf("ServerInfo.Name = %q, want %q", result.ServerInfo.Name, "test")
	}
	if result.Capabilities.Tools == nil || !result.Capabilities.Tools.ListChanged {
		t.Errorf("Capabilities not echoed correctly: %+v", result.Capabilities)
	}
}

func TestDispatch_ToolsCallMissingCapReturns32003(t *testing.T) {
	// The dispatcher should translate a typed *core.MissingCapabilityError
	// from a tool handler into a JSON-RPC -32003 response with the
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
			"io.modelcontextprotocol/protocolVersion": "DRAFT-2026-v1",
			"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": {}
		}
	}`)
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("5"),
		Method:  "tools/call",
		Params:  params,
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil || resp.Error == nil {
		t.Fatal("expected -32003 for missing-capability tool")
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Errorf("got code %d, want %d (-32003)",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability)
	}
	if got := HTTPStatusForCode(resp.Error.Code); got != 400 {
		t.Errorf("HTTPStatusForCode(-32003) = %d, want 400", got)
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
		Params:  validMetaParams(t),
	}
	resp := d.Dispatch(context.Background(), req)
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
	meta := &core.RequestMeta{ProtocolVersion: "DRAFT-2026-v1"}
	ctx := core.WithRequestMeta(context.Background(), meta)
	if got := RequestMetaFromContext(ctx); got != meta {
		t.Errorf("RequestMetaFromContext: got %+v, want %+v", got, meta)
	}
}

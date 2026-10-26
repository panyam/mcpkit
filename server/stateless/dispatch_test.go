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
	authErr  error             // when non-nil, InvokeWithMiddleware short-circuits with it
	invoked  []string          // methods that reached InvokeWithMiddleware
	lastMeta *core.RequestMeta // the _meta envelope the last call carried on ctx
}

func (f *fakeBackend) ServerInfo() core.ServerInfo           { return f.info }
func (f *fakeBackend) Instructions() string                  { return "" }
func (f *fakeBackend) Capabilities() core.ServerCapabilities { return f.caps }
func (f *fakeBackend) SupportedVersions() []string {
	if f.versions == nil {
		return []string{core.DraftProtocolVersion2026V1}
	}
	return f.versions
}

// InvokeWithMiddleware stands in for the server's chain and method table. It
// answers server/discover and the three list methods, and -32601 otherwise.
// The authErr field models a middleware short-circuit so
// dispatch_autherror_test.go can assert the error propagates out of Dispatch
// unfolded.
func (f *fakeBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error) {
	f.invoked = append(f.invoked, req.Method)
	f.lastMeta = RequestMetaFromContext(ctx)
	if f.authErr != nil {
		return nil, f.authErr
	}
	switch req.Method {
	case "server/discover":
		return core.NewResponse(req.ID, NewDiscoverResult(f)), nil
	case "tools/list", "resources/list", "prompts/list":
		return core.NewResponse(req.ID, map[string]any{}), nil
	}
	return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "method not found: "+req.Method), nil
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

// TestDispatch_EveryMethodGoesThroughTheBackend pins #1625: past the envelope
// checks there is one route, so server/discover and an unknown method reach
// the backend's chain exactly like tools/list does, and a removed method or a
// bad envelope never does.
func TestDispatch_EveryMethodGoesThroughTheBackend(t *testing.T) {
	for _, tc := range []struct {
		method string
		params json.RawMessage
		want   bool
	}{
		{"server/discover", validMetaParams(t), true},
		{"tools/list", validMetaParams(t), true},
		{"frobnicate", validMetaParams(t), true},
		{"initialize", validMetaParams(t), false},
		{"tools/list", json.RawMessage(`{}`), false},
	} {
		f := &fakeBackend{}
		New(f).Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: tc.method, Params: core.NewRawJSON(tc.params),
		})
		if got := len(f.invoked) == 1 && f.invoked[0] == tc.method; got != tc.want {
			t.Errorf("%s with params %s: backend saw %v, want reached=%v", tc.method, tc.params, f.invoked, tc.want)
		}
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

// 2026-07-28 final revision: every result carries the generalized resultType
// discriminator, and DiscoverResult additionally carries the SEP-2549 caching
// hints as required fields.
func TestDispatch_GeneralizedResultType(t *testing.T) {
	caps := core.ServerCapabilities{Tools: &core.ToolsCap{}}
	info := core.ServerInfo{Name: "test", Version: "0.0.1"}
	d := New(&fakeBackend{info: info, caps: caps})

	t.Run("discover carries resultType and caching hints", func(t *testing.T) {
		req := &core.Request{
			JSONRPC: "2.0",
			ID:      json.RawMessage("40"),
			Method:  "server/discover",
			Params:  core.NewRawJSON(validMetaParams(t)),
		}
		resp, _ := d.Dispatch(context.Background(), req)
		if resp == nil || resp.Error != nil {
			t.Fatalf("server/discover failed: %+v", resp)
		}
		raw, _ := json.Marshal(resp.Result)
		var got map[string]any
		json.Unmarshal(raw, &got)
		if got["resultType"] != "complete" {
			t.Errorf("resultType = %v, want complete", got["resultType"])
		}
		if _, ok := got["ttlMs"]; !ok {
			t.Error("ttlMs missing from DiscoverResult")
		}
		if got["cacheScope"] != "public" {
			t.Errorf("cacheScope = %v, want public", got["cacheScope"])
		}
	})

	t.Run("tools list carries resultType", func(t *testing.T) {
		req := &core.Request{
			JSONRPC: "2.0",
			ID:      json.RawMessage("41"),
			Method:  "tools/list",
			Params:  core.NewRawJSON(validMetaParams(t)),
		}
		resp, _ := d.Dispatch(context.Background(), req)
		if resp == nil || resp.Error != nil {
			t.Fatalf("tools/list failed: %+v", resp)
		}
		raw, _ := json.Marshal(resp.Result)
		var got map[string]any
		json.Unmarshal(raw, &got)
		if got["resultType"] != "complete" {
			t.Errorf("resultType = %v, want complete", got["resultType"])
		}
	})
}

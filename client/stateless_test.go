package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

func TestClientModeString(t *testing.T) {
	cases := []struct {
		mode ClientMode
		want string
	}{
		{ClientModeLegacyOnly, "legacy"},
		{ClientModeAdaptive, "adaptive"},
		{ClientModeStateless, "stateless"},
		{ClientMode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("ClientMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestParseClientMode(t *testing.T) {
	cases := []struct {
		in     string
		want   ClientMode
		wantOK bool
	}{
		{"legacy", ClientModeLegacyOnly, true},
		{"adaptive", ClientModeAdaptive, true},
		{"stateless", ClientModeStateless, true},
		{"ADAPTIVE", ClientModeAdaptive, true},
		{"  Legacy  ", ClientModeLegacyOnly, true},
		{"", ClientModeAdaptive, false},
		{"nope", ClientModeAdaptive, false},
	}
	for _, c := range cases {
		got, ok := ParseClientMode(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseClientMode(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if ok && got != c.want {
			t.Errorf("ParseClientMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveClientMode_DefaultIsLegacyOnly(t *testing.T) {
	t.Setenv(ClientModeEnvVar, "")
	// Default is LegacyOnly for backward compat on upgrade. Adaptive
	// is the migration-path opt-in via WithClientMode or env.
	if got := ResolveClientMode(); got != ClientModeLegacyOnly {
		t.Errorf("shipping-default ResolveClientMode = %v, want LegacyOnly", got)
	}
}

func TestResolveClientMode_EnvBeatsDefault(t *testing.T) {
	t.Setenv(ClientModeEnvVar, "stateless")
	prev := DefaultClientMode
	SetDefaultClientMode(ClientModeLegacyOnly)
	t.Cleanup(func() { SetDefaultClientMode(prev) })

	if got := ResolveClientMode(); got != ClientModeStateless {
		t.Errorf("ResolveClientMode with env=stateless = %v, want Stateless", got)
	}
}

func TestWrapParamsForStatelessWire_NoOpUnlessStateless(t *testing.T) {
	c := &Client{info: core.ClientInfo{Name: "t", Version: "1"}}
	// useStatelessWire is false — params pass through.
	got := c.wrapParamsForStatelessWire(map[string]any{"x": 1})
	m, _ := got.(map[string]any)
	if _, hasMeta := m["_meta"]; hasMeta {
		t.Errorf("expected no _meta added in legacy mode, got %+v", m)
	}
}

func TestWrapParamsForStatelessWire_AddsEnvelope(t *testing.T) {
	c := &Client{
		info:              core.ClientInfo{Name: "t", Version: "1"},
		useStatelessWire:  true,
		negotiatedVersion: core.DraftProtocolVersion2026V1,
	}
	got := c.wrapParamsForStatelessWire(map[string]any{"name": "echo"})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	if m["name"] != "echo" {
		t.Errorf("caller param dropped: %+v", m)
	}
	meta, _ := m["_meta"].(map[string]any)
	if meta == nil {
		t.Fatalf("expected _meta, got %+v", m)
	}
	if meta[core.MetaKeyProtocolVersion] != core.DraftProtocolVersion2026V1 {
		t.Errorf("protocolVersion = %v, want %v",
			meta[core.MetaKeyProtocolVersion], core.DraftProtocolVersion2026V1)
	}
}

func TestWrapParamsForStatelessWire_PreservesCallerMeta(t *testing.T) {
	c := &Client{
		info:              core.ClientInfo{Name: "t", Version: "1"},
		useStatelessWire:  true,
		negotiatedVersion: core.DraftProtocolVersion2026V1,
	}
	in := map[string]any{
		"_meta": map[string]any{
			// Caller's extension-namespaced meta key — must survive.
			"io.example/customKey": "preserved",
		},
		"x": 1,
	}
	got := c.wrapParamsForStatelessWire(in)
	m := got.(map[string]any)
	meta := m["_meta"].(map[string]any)
	if meta["io.example/customKey"] != "preserved" {
		t.Errorf("caller meta key dropped: %+v", meta)
	}
	if meta[core.MetaKeyProtocolVersion] != core.DraftProtocolVersion2026V1 {
		t.Errorf("envelope key not added: %+v", meta)
	}
}

func TestTryDecodeJSONRPC(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"valid error body", `{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"x"}}`, true},
		{"valid result body", `{"jsonrpc":"2.0","id":1,"result":{}}`, true},
		{"missing jsonrpc", `{"id":1,"error":{"code":-32020}}`, false},
		{"plain text", `<html>...`, false},
		{"empty", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tryDecodeJSONRPC([]byte(c.body))
			if (got != nil) != c.want {
				t.Errorf("tryDecodeJSONRPC(%q) = %v, want non-nil=%v", c.body, got, c.want)
			}
		})
	}
}

// fakeMCPServer is a minimal HTTP server that responds to JSON-RPC POSTs.
// Behavior is per-handler so tests can simulate stateless / legacy / error servers.
type fakeMCPServer struct {
	handler  func(method string, params json.RawMessage) (status int, body []byte)
	requests int32 // observed request count for assertions
}

func (f *fakeMCPServer) start() (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.requests, 1)
		body, _ := io.ReadAll(r.Body)
		var req core.Request
		_ = json.Unmarshal(body, &req)
		status, resBody := f.handler(req.Method, req.Params.Raw())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(resBody)
	}))
	return s, s.URL
}

// TestClient_AdaptiveAgainstStatelessServer verifies that an Adaptive
// client probes server/discover, gets a successful response, and skips
// the legacy initialize handshake entirely. ServerInfo is populated from
// the discover result.
func TestClient_AdaptiveAgainstStatelessServer(t *testing.T) {
	saw := map[string]int{}
	fake := &fakeMCPServer{
		handler: func(method string, _ json.RawMessage) (int, []byte) {
			saw[method]++
			switch method {
			case "server/discover":
				// Spec PR 3002 shape: server identity in the result _meta.
				body, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{
						"supportedVersions": []string{core.DraftProtocolVersion2026V1},
						"capabilities":      map[string]any{},
						"_meta": map[string]any{
							core.MetaKeyServerInfo: map[string]any{"name": "stateless", "version": "0.0.1"},
						},
					},
				})
				return 200, body
			}
			return 500, []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"unexpected"}}`)
		},
	}
	ts, url := fake.start()
	defer ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeAdaptive))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !c.useStatelessWire {
		t.Errorf("useStatelessWire = false, want true (adaptive should classify as stateless)")
	}
	if c.ServerInfo.Name != "stateless" {
		t.Errorf("ServerInfo.Name = %q, want %q", c.ServerInfo.Name, "stateless")
	}
	if saw["initialize"] != 0 {
		t.Errorf("legacy initialize sent under stateless-server probe; saw=%v", saw)
	}
	if saw["server/discover"] != 1 {
		t.Errorf("expected 1 server/discover, got %d", saw["server/discover"])
	}
}

// TestClient_DiscoverBodyServerInfoFallback verifies compatibility with a
// server built against the pre-PR-3002 draft shape, which emits serverInfo
// in the discover result body instead of _meta. The client still captures
// the identity.
func TestClient_DiscoverBodyServerInfoFallback(t *testing.T) {
	fake := &fakeMCPServer{
		handler: func(method string, _ json.RawMessage) (int, []byte) {
			if method == "server/discover" {
				body, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{
						"supportedVersions": []string{core.DraftProtocolVersion2026V1},
						"capabilities":      map[string]any{},
						"serverInfo":        map[string]any{"name": "pre-3002", "version": "0.0.1"},
					},
				})
				return 200, body
			}
			return 500, []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"unexpected"}}`)
		},
	}
	ts, url := fake.start()
	defer ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeAdaptive))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.ServerInfo.Name != "pre-3002" {
		t.Errorf("ServerInfo.Name = %q, want %q (body fallback)", c.ServerInfo.Name, "pre-3002")
	}
}

// TestClient_AdaptiveAgainstLegacyServer verifies the fallback path:
// when server/discover returns -32601 (in a 200 body, as a legacy server
// would emit for an unknown method), the adaptive client falls back to
// the legacy initialize handshake transparently.
func TestClient_AdaptiveAgainstLegacyServer(t *testing.T) {
	saw := map[string]int{}
	fake := &fakeMCPServer{
		handler: func(method string, _ json.RawMessage) (int, []byte) {
			saw[method]++
			switch method {
			case "server/discover":
				// Legacy server: unknown method, body says -32601.
				body, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"error": map[string]any{
						"code": core.ErrCodeMethodNotFound, "message": "no such method",
					},
				})
				return 200, body
			case "initialize":
				body, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": 2,
					"result": map[string]any{
						"protocolVersion": "2025-11-25",
						"capabilities":    map[string]any{},
						"serverInfo":      map[string]any{"name": "legacy", "version": "0.0.1"},
					},
				})
				return 200, body
			}
			return 200, []byte(`{"jsonrpc":"2.0","id":3,"result":{}}`)
		},
	}
	ts, url := fake.start()
	defer ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeAdaptive))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.useStatelessWire {
		t.Errorf("useStatelessWire = true, want false (legacy server should fall back)")
	}
	if c.ServerInfo.Name != "legacy" {
		t.Errorf("ServerInfo.Name = %q, want %q", c.ServerInfo.Name, "legacy")
	}
	if saw["server/discover"] != 1 {
		t.Errorf("expected 1 server/discover probe, got %d", saw["server/discover"])
	}
	if saw["initialize"] != 1 {
		t.Errorf("expected 1 initialize after fallback, got %d", saw["initialize"])
	}
}

// TestClient_StatelessModeDiscoverlessBestEffort verifies that
// ClientModeStateless treats server/discover as an optional optimization, not a
// precondition (issue 829). The SEP-2575 draft lets a stateless client begin
// with any request, so a server that returns -32601 for server/discover is
// still reachable: Connect succeeds, marks the stateless wire, and leaves
// ServerInfo unpopulated for the first real request to fill in.
func TestClient_StatelessModeDiscoverlessBestEffort(t *testing.T) {
	fake := &fakeMCPServer{
		handler: func(method string, _ json.RawMessage) (int, []byte) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{
					"code": core.ErrCodeMethodNotFound, "message": "no such method",
				},
			})
			return 200, body
		},
	}
	ts, url := fake.start()
	defer ts.Close()

	c := NewClient(url, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeStateless))
	if err := c.Connect(); err != nil {
		t.Fatalf("stateless Connect against a discover-less server should succeed, got %v", err)
	}
	if !c.useStatelessWire {
		t.Error("expected client to be on the stateless wire")
	}
	if c.ServerInfo.Name != "" {
		t.Errorf("expected unpopulated ServerInfo, got name=%q", c.ServerInfo.Name)
	}
}

// TestClient_StatelessWire_SendsMetaAndHeader verifies the integration:
// once classified as stateless, every outgoing call carries the _meta
// envelope and the MCP-Protocol-Version HTTP header.
func TestClient_StatelessWire_SendsMetaAndHeader(t *testing.T) {
	type observed struct {
		method  string
		params  json.RawMessage
		header  string
	}
	var seen []observed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req core.Request
		_ = json.Unmarshal(body, &req)
		seen = append(seen, observed{
			method: req.Method,
			params: req.Params.Raw(),
			header: r.Header.Get(core.HTTPProtocolVersionHeader),
		})
		switch req.Method {
		case "server/discover":
			out, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{
					"supportedVersions": []string{core.DraftProtocolVersion2026V1},
					"capabilities":      map[string]any{},
					"serverInfo":        map[string]any{"name": "x", "version": "1"},
				},
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.Write(out)
		case "tools/list":
			out, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 2,
				"result":  map[string]any{"tools": []any{}},
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.Write(out)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeAdaptive))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// Find the tools/list observation.
	var toolsObs *observed
	for i := range seen {
		if seen[i].method == "tools/list" {
			toolsObs = &seen[i]
			break
		}
	}
	if toolsObs == nil {
		t.Fatalf("no tools/list observed; seen=%+v", seen)
	}
	if toolsObs.header != core.DraftProtocolVersion2026V1 {
		t.Errorf("tools/list MCP-Protocol-Version header = %q, want %q",
			toolsObs.header, core.DraftProtocolVersion2026V1)
	}
	var params map[string]any
	if err := json.Unmarshal(toolsObs.params, &params); err != nil {
		t.Fatalf("decode tools/list params: %v", err)
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		t.Fatalf("tools/list params missing _meta envelope: %s", string(toolsObs.params))
	}
	if meta[core.MetaKeyProtocolVersion] != core.DraftProtocolVersion2026V1 {
		t.Errorf("tools/list _meta protocolVersion = %v, want %v",
			meta[core.MetaKeyProtocolVersion], core.DraftProtocolVersion2026V1)
	}
}

// TestClient_4xxJSONRPCErrorParsed verifies that a SEP-2575 server's
// 400 response with a JSON-RPC error body surfaces to the caller as a
// JSON-RPC error (not an HTTPStatusError swallow).
func TestClient_4xxJSONRPCErrorParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req core.Request
		_ = json.Unmarshal(body, &req)
		if req.Method == "server/discover" {
			out, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{
					"supportedVersions": []string{core.DraftProtocolVersion2026V1},
					"capabilities":      map[string]any{},
					"serverInfo":        map[string]any{"name": "x", "version": "1"},
				},
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.Write(out)
			return
		}
		// Simulate a -32022 + HTTP 400 from a SEP-2575 server.
		out, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{
				"code": core.ErrCodeUnsupportedProtocolVersion, "message": "bad version",
			},
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write(out)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, core.ClientInfo{Name: "t", Version: "1"}, WithClientMode(ClientModeAdaptive))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err := c.ListTools(t.Context())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should be a JSON-RPC error with -32022, not an HTTPStatusError.
	if _, ok := err.(*HTTPStatusError); ok {
		t.Errorf("got HTTPStatusError, want JSON-RPC error: %v", err)
	}
	if !strings.Contains(err.Error(), "bad version") {
		t.Errorf("error message = %q, want containing 'bad version'", err.Error())
	}
}

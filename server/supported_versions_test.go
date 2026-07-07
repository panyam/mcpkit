package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// newVersionServer builds a Streamable HTTP server restricted to the given
// protocol versions (issue 419) with one trivial tool so tools/list works.
func newVersionServer(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "ver", Version: "1"}, server.WithSupportedVersions(versions...))
	srv.RegisterTool(
		core.ToolDef{Name: "noop", Description: "noop", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil },
	)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

// Test_Issue419_InitializeNegotiatesWithinConfiguredSet: a server configured to
// drop 2024-11-05 negotiates a client's 2024-11-05 request DOWN to the
// configured preferred (2025-11-25), rather than echoing the excluded version.
func Test_Issue419_InitializeNegotiatesWithinConfiguredSet(t *testing.T) {
	ts := newVersionServer(t, "2025-11-25", "2025-03-26")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`
	resp := postJSON(t, ts.URL+"/mcp", body, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode (%s): %v", raw, err)
	}
	if out.Result.ProtocolVersion != "2025-11-25" {
		t.Errorf("negotiated %q, want configured preferred 2025-11-25 (excluded 2024-11-05 should not be echoed); raw=%s",
			out.Result.ProtocolVersion, raw)
	}
}

// Test_Issue419_ConfiguredVersionNegotiatesAsIs: a client requesting a version
// that IS in the configured set gets exactly that version.
func Test_Issue419_ConfiguredVersionNegotiatesAsIs(t *testing.T) {
	ts := newVersionServer(t, "2025-11-25", "2025-03-26")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`
	resp := postJSON(t, ts.URL+"/mcp", body, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &out)
	if out.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("negotiated %q, want 2025-03-26 (in the configured set); raw=%s", out.Result.ProtocolVersion, raw)
	}
}

// Test_Issue419_HeaderOutsideConfiguredSetRejected: after negotiating, a
// post-initialize request whose MCP-Protocol-Version header carries a version
// the server was configured to drop is rejected with HTTP 400.
func Test_Issue419_HeaderOutsideConfiguredSetRejected(t *testing.T) {
	ts := newVersionServer(t, "2025-11-25")
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`
	initResp := postJSON(t, ts.URL+"/mcp", initBody, nil)
	sid := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	if sid == "" {
		t.Fatal("no session id from initialize")
	}
	// notifications/initialized to complete the handshake.
	postJSON(t, ts.URL+"/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"Mcp-Session-Id": sid}).Body.Close()

	// A post-init request carrying an excluded version in the header → 400.
	resp := postJSON(t, ts.URL+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{
		"Mcp-Session-Id":       sid,
		"MCP-Protocol-Version": "2024-11-05",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("want HTTP 400 for excluded MCP-Protocol-Version header, got %d; body=%s", resp.StatusCode, b)
	}
}

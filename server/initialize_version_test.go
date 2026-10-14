package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

// Test_Issue422_ProtocolVersionHeaderBodyMismatch asserts that when the
// MCP-Protocol-Version header and the initialize body's protocolVersion are
// both present and disagree, the server rejects with HTTP 400 before
// negotiation — instead of silently honoring the body.
func Test_Issue422_ProtocolVersionHeaderBodyMismatch(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := func(ver string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`, ver)
	}

	cases := []struct {
		name       string
		header     string // "" = omit MCP-Protocol-Version header
		bodyVer    string
		wantStatus int
	}{
		{"mismatch", "2024-11-05", "2025-03-26", http.StatusBadRequest},
		{"match", "2025-03-26", "2025-03-26", http.StatusOK},
		{"header-omitted", "", "2025-03-26", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body(tc.bodyVer)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			if tc.header != "" {
				req.Header.Set("MCP-Protocol-Version", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("want status %d, got %d", tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

// A Streamable HTTP client on 2025-06-18 sends that version in the
// MCP-Protocol-Version header, which the server must accept rather than 400
// (#1519).
func Test_Issue1519_ProtocolVersionHeader20250618(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want status 200, got %d", resp.StatusCode)
	}
	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q, want 2025-06-18", out.Result.ProtocolVersion)
	}
}

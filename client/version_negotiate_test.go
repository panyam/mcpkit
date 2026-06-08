package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestPickSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		name             string
		server           []string
		client           []string
		want             string
	}{
		{"single-match", []string{"2026-07-28"}, []string{"2026-07-28"}, "2026-07-28"},
		{"client-preference-newest-first", []string{"2024-11-05", "2026-07-28"}, []string{"2026-07-28", "2024-11-05"}, "2026-07-28"},
		{"server-only-old", []string{"2024-11-05"}, []string{"2026-07-28"}, ""},
		{"empty-server", []string{}, []string{"2026-07-28"}, ""},
		{"empty-client", []string{"2026-07-28"}, []string{}, ""},
		{"no-overlap", []string{"2025-03-26"}, []string{"2026-07-28"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickSupportedVersion(tc.server, tc.client); got != tc.want {
				t.Errorf("pick(server=%v, client=%v) = %q, want %q", tc.server, tc.client, got, tc.want)
			}
		})
	}
}

func TestIsUnsupportedVersionError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resp      *rpcResponse
		wantRetry bool
		wantPick  string
	}{
		{"nil-resp", nil, false, ""},
		{"no-error", &rpcResponse{}, false, ""},
		{
			"32001-with-supported-draft",
			&rpcResponse{Error: &core.Error{
				Code: -32001, Message: "Unsupported protocol version",
				Data: map[string]any{"supported": []any{"2026-07-28"}},
			}},
			true, "2026-07-28",
		},
		{
			"32004-with-supported-draft",
			&rpcResponse{Error: &core.Error{
				Code: -32004, Message: "UnsupportedVersion",
				Data: map[string]any{"supported": []any{"2026-07-28"}},
			}},
			true, "2026-07-28",
		},
		{
			"32001-header-mismatch-no-supported",
			&rpcResponse{Error: &core.Error{
				Code: -32001, Message: "header mismatch",
				Data: map[string]any{"header": "Mcp-Method", "expected": "tools/list", "received": "tools/call"},
			}},
			false, "",
		},
		{
			"32004-supported-no-overlap",
			&rpcResponse{Error: &core.Error{
				Code: -32004, Message: "UnsupportedVersion",
				Data: map[string]any{"supported": []any{"2024-11-05"}},
			}},
			false, "",
		},
		{
			"other-error-code-with-supported",
			&rpcResponse{Error: &core.Error{
				Code: -32603, Message: "internal error",
				Data: map[string]any{"supported": []any{"2026-07-28"}},
			}},
			false, "",
		},
		{
			"typed-string-slice-supported",
			&rpcResponse{Error: &core.Error{
				Code: -32004,
				Data: map[string]any{"supported": []string{"2026-07-28"}},
			}},
			true, "2026-07-28",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotRetry, gotPick := isUnsupportedVersionError(tc.resp)
			if gotRetry != tc.wantRetry || gotPick != tc.wantPick {
				t.Errorf("got (retry=%v, pick=%q), want (retry=%v, pick=%q)",
					gotRetry, gotPick, tc.wantRetry, tc.wantPick)
			}
		})
	}
}

// End-to-end retry: a stateless-wire client should retry exactly once
// when the server returns -32001 + data.supported on the first POST, and
// observe a SUCCESS on the second. Mirrors the upstream request-metadata
// conformance scenario's retry probe (closes #523's WARNING).
func TestRawCallRetryOnUnsupportedVersion(t *testing.T) {
	var postCount atomic.Int32
	var firstVersionHeader, secondVersionHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req core.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		count := postCount.Add(1)

		// First request: respond server/discover so Connect() succeeds,
		// then on the first follow-up call return -32001 + supported list.
		if req.Method == "server/discover" {
			firstVersionHeader.Store(r.Header.Get(core.HTTPProtocolVersionHeader))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["%s"],"capabilities":{},"serverInfo":{"name":"t","version":"1"}}}`,
				string(req.ID), core.DraftProtocolVersion2026V1)))
			return
		}

		if count == 2 {
			// First non-discover POST: reject with -32001 + supported.
			firstVersionHeader.Store(r.Header.Get(core.HTTPProtocolVersionHeader))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":%s,"error":{"code":-32001,"message":"Unsupported protocol version","data":{"supported":["%s"]}}}`,
				string(req.ID), core.DraftProtocolVersion2026V1)))
			return
		}

		// Retry attempt — should succeed.
		secondVersionHeader.Store(r.Header.Get(core.HTTPProtocolVersionHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, string(req.ID))))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, core.ClientInfo{Name: "retry-test", Version: "1"},
		WithClientMode(ClientModeStateless))
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	if _, err := c.ListTools(); err != nil {
		t.Fatalf("ListTools after retry: %v", err)
	}

	// Discover + first attempt + retry = 3 POSTs.
	if got := postCount.Load(); got != 3 {
		t.Errorf("POST count = %d, want 3 (discover + first + retry)", got)
	}
	if v, _ := secondVersionHeader.Load().(string); v != core.DraftProtocolVersion2026V1 {
		t.Errorf("retry MCP-Protocol-Version = %q, want %q", v, core.DraftProtocolVersion2026V1)
	}
}

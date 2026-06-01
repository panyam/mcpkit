package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Client-only SEP-2243 glue: setSEP2243RoutingHeaders + the end-to-end
// streamable-transport check. The pure wire-format helpers (extract /
// validate / encode / DeriveMcpName / NeedsBase64Encoding) are tested
// against the public API in core/sep2243_test.go.

func TestSetSEP2243RoutingHeaders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		cc         *CallContext
		wantMethod string
		wantName   string
	}{
		{"method-only-nil-cc", "tools/list", nil, "tools/list", ""},
		{"method-and-name", "tools/call", &CallContext{mcpName: "echo"}, "tools/call", "echo"},
		{"empty-method-skipped", "", &CallContext{mcpName: "echo"}, "", "echo"},
		{"cc-without-name", "ping", &CallContext{}, "ping", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			setSEP2243RoutingHeaders(req, tc.method, tc.cc)
			if got := req.Header.Get("Mcp-Method"); got != tc.wantMethod {
				t.Errorf("Mcp-Method = %q, want %q", got, tc.wantMethod)
			}
			if got := req.Header.Get("Mcp-Name"); got != tc.wantName {
				t.Errorf("Mcp-Name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

// End-to-end: a streamable POST through the client should land at the server
// with Mcp-Method (always) and Mcp-Name (when applicable) set. This guards
// the wire-level contract that SEP-2243 routing middleware depends on.
func TestStreamableTransportEmitsSEP2243Headers(t *testing.T) {
	type captured struct {
		method, mcpMethod, mcpName string
	}
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.mcpMethod = r.Header.Get("Mcp-Method")
		got.mcpName = r.Header.Get("Mcp-Name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	t.Run("notify-without-name", func(t *testing.T) {
		got = captured{}
		tr := newStreamableClientTransport(srv.URL, nil)
		data := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
		if err := tr.notify("notifications/initialized", data); err != nil {
			t.Fatalf("notify: %v", err)
		}
		if got.mcpMethod != "notifications/initialized" {
			t.Errorf("Mcp-Method = %q, want %q", got.mcpMethod, "notifications/initialized")
		}
		if got.mcpName != "" {
			t.Errorf("Mcp-Name = %q, want empty (notify path never sets Mcp-Name)", got.mcpName)
		}
	})

	t.Run("call-with-name-via-cc", func(t *testing.T) {
		got = captured{}
		tr := newStreamableClientTransport(srv.URL, nil)
		data := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`)
		cc := &CallContext{mcpName: "echo"}
		if _, err := tr.callWithContext("tools/call", data, cc); err != nil {
			t.Fatalf("callWithContext: %v", err)
		}
		if got.mcpMethod != "tools/call" {
			t.Errorf("Mcp-Method = %q, want %q", got.mcpMethod, "tools/call")
		}
		if got.mcpName != "echo" {
			t.Errorf("Mcp-Name = %q, want %q", got.mcpName, "echo")
		}
	})
}

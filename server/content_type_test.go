package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
)

// TestStreamableHTTPRejectsNonJSONContentType verifies that POST requests
// to the Streamable HTTP transport are rejected with 415 Unsupported Media
// Type when the Content-Type header is not application/json. This is a
// defense-in-depth measure against CSRF via cross-origin form submissions,
// which browsers send as application/x-www-form-urlencoded without CORS
// preflight.
func TestStreamableHTTPRejectsNonJSONContentType(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	cases := []struct {
		name        string
		contentType string
		wantStatus  int
	}{
		{"form-urlencoded", "application/x-www-form-urlencoded", 415},
		{"text-plain", "text/plain", 415},
		{"missing", "", 415},
		{"json", "application/json", 200},
		{"json-with-charset", "application/json; charset=utf-8", 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
			req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			assert.Equal(t, tc.wantStatus, resp.StatusCode)
		})
	}
}

// TestSSETransportRejectsNonJSONContentType verifies that POST /message
// requests to the SSE transport are rejected with 415 when Content-Type
// is not application/json.
func TestSSETransportRejectsNonJSONContentType(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// POST with form content type should be rejected
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp/message?sessionId=test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	assert.Equal(t, 415, resp.StatusCode)
}

// TestGETRequestsNotAffectedByContentTypeCheck verifies that GET requests
// (SSE stream, health check) are not affected by Content-Type enforcement.
func TestGETRequestsNotAffectedByContentTypeCheck(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// GET should work without Content-Type (it's a GET, no body)
	req, _ := http.NewRequest("GET", ts.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	// Should not be 415 — GET doesn't require Content-Type
	assert.NotEqual(t, 415, resp.StatusCode)
}

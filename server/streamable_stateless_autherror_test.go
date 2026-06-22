package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// A server.Middleware that short-circuits a tools/call with *core.AuthError on
// the SEP-2575 stateless wire must produce HTTP 403 + the WWW-Authenticate
// header — the same shape the legacy wire emits via writeAuthError — not a
// generic -32603 JSON-RPC body over HTTP 200 (issue 815). This is the end-to-
// end transport coverage for the scope-challenge / step-up auth flow that the
// stateless dispatch path previously dropped.
func TestStatelessTransport_MiddlewareAuthErrorYields403(t *testing.T) {
	const challenge = `Bearer error="insufficient_scope", scope="docs:write"`

	scopeGate := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		if req.Method == "tools/call" {
			return nil, &core.AuthError{
				Code:            http.StatusForbidden,
				Message:         "insufficient scope",
				WWWAuthenticate: challenge,
			}
		}
		return next(ctx, req)
	}

	s := NewServer(core.ServerInfo{Name: "stateless-autherr", Version: "0.0.1"}, WithMiddleware(scopeGate))
	if err := s.Registry().AddTool(
		core.ToolDef{Name: "docs_write", Description: "needs docs:write"},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("should never run"), nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	defer ts.Close()
	url := ts.URL + "/mcp"

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "docs_write",
			"arguments": map[string]any{},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("HTTP status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != challenge {
		t.Errorf("WWW-Authenticate = %q, want %q", got, challenge)
	}
}

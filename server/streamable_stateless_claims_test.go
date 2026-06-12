package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// TestStateless_ClaimsThreadedToHandlerCtx covers the SEP-2575 fix that
// installs the CheckAuth-produced claims onto the handler ctx. Before
// the fix, statelessBackend.callToolForStateless built the handler ctx
// without claims (server/streamable_stateless.go literally dropped the
// claims parameter), so ctx.AuthClaims() returned nil even when the
// bearer token was valid — events/subscribe and any other handler that
// gates on identity would falsely reject authenticated callers.
func TestStateless_ClaimsThreadedToHandlerCtx(t *testing.T) {
	const wantSubject = "user-stateless-claims"
	validator := &testClaimsValidator{
		validToken: "good-token",
		claims:     &core.Claims{Subject: wantSubject, Scopes: []string{"read"}},
	}

	s := NewServer(core.ServerInfo{Name: "stateless-claims-test", Version: "0.0.1"},
		WithAuth(validator),
	)
	if err := s.Registry().AddTool(
		core.ToolDef{Name: "who"},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			subj := ""
			if claims := ctx.AuthClaims(); claims != nil {
				subj = claims.Subject
			}
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: subj}}}, nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"name":      "who",
			"arguments": map[string]any{},
		},
	}
	resp := postStatelessJSON(t, ts.URL+"/mcp", body, map[string]string{
		"Authorization": "Bearer good-token",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	r := decode(t, resp)
	if r.Error != nil {
		t.Fatalf("response carried error: %v", r.Error)
	}

	raw, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var result struct {
		Content []core.Content `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != wantSubject {
		got := ""
		if len(result.Content) > 0 {
			got = result.Content[0].Text
		}
		t.Fatalf("handler saw subject = %q, want %q (claims dropped on stateless dispatch?)",
			got, wantSubject)
	}
}

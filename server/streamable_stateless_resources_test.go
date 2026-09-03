package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// statelessMeta is the SEP-2575 in-band handshake every stateless request
// carries in place of an initialize round trip.
func statelessMeta() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    draftVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func statelessServerWithResources(t *testing.T, opts ...Option) *Server {
	t.Helper()
	s := NewServer(core.ServerInfo{Name: "stateless-resources", Version: "0.0.1"}, opts...)
	s.RegisterResource(
		core.ResourceDef{URI: "test://static", Name: "static", MimeType: "text/plain"},
		func(_ core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "static body",
			}}}, nil
		},
	)
	s.RegisterResourceTemplate(
		core.ResourceTemplate{URITemplate: "test://tmpl/{id}/data", Name: "tmpl", MimeType: "text/plain"},
		func(_ core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: uri, MimeType: "text/plain", Text: "expanded " + params["id"],
			}}}, nil
		},
	)
	return s
}

func readResourceStateless(t *testing.T, url, uri string) *http.Response {
	t.Helper()
	return postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "resources/read",
		"params": map[string]any{"uri": uri, "_meta": statelessMeta()},
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
}

// Middleware must run for resources/read on the stateless wire, exactly as it
// does for tools/call and prompts/get.
//
// Before this was fixed, handleResourcesRead bypassed the chain entirely and
// InvokeWithMiddleware had no resources/read case, so a middleware gate
// applied to tools and prompts and silently did not apply to resources. For an
// authorization middleware that is a bypass, not a missing feature: a caller
// refused a tool could ask for the resource instead. This test fails on the
// pre-fix code with HTTP 200 and the resource body.
func TestStatelessTransport_MiddlewareRunsForResourcesRead(t *testing.T) {
	const challenge = `Bearer error="insufficient_scope", scope="files:read"`

	gate := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		if req.Method == "resources/read" {
			return nil, &core.AuthError{
				Code:            http.StatusForbidden,
				Message:         "insufficient scope",
				WWWAuthenticate: challenge,
			}
		}
		return next(ctx, req)
	}

	s := statelessServerWithResources(t, WithMiddleware(gate))
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	defer ts.Close()

	for _, uri := range []string{"test://static", "test://tmpl/42/data"} {
		t.Run(uri, func(t *testing.T) {
			resp := readResourceStateless(t, ts.URL+"/mcp", uri)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 403 (middleware did not run); body: %s", resp.StatusCode, body)
			}
			if got := resp.Header.Get("WWW-Authenticate"); got != challenge {
				t.Errorf("WWW-Authenticate = %q, want %q", got, challenge)
			}
		})
	}
}

// A templated resource must resolve on the stateless wire, and resolve to the
// same definition the session wire picks. Template matching was previously
// unimplemented here and every templated read returned -32602.
func TestStatelessTransport_TemplatedResourceRead(t *testing.T) {
	s := statelessServerWithResources(t)
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	defer ts.Close()

	tests := []struct{ uri, wantText string }{
		{"test://static", "static body"},
		{"test://tmpl/42/data", "expanded 42"},
		{"test://tmpl/abc/data", "expanded abc"},
	}
	for _, tc := range tests {
		t.Run(tc.uri, func(t *testing.T) {
			resp := readResourceStateless(t, ts.URL+"/mcp", tc.uri)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.wantText) {
				t.Fatalf("body missing %q; got: %s", tc.wantText, body)
			}
		})
	}
}

// An unmatched URI must still report unknown resource rather than falling
// through to something else now that a template path exists.
func TestStatelessTransport_UnknownResourceStillErrors(t *testing.T) {
	s := statelessServerWithResources(t)
	ts := httptest.NewServer(s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	defer ts.Close()

	resp := readResourceStateless(t, ts.URL+"/mcp", "test://nope")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw := string(body)
	if i := strings.Index(raw, "{"); i >= 0 {
		_ = json.Unmarshal([]byte(raw[i:]), &env)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "unknown resource") {
		t.Fatalf("want an unknown-resource error, got: %s", raw)
	}
}

// Package testutil provides test helpers for MCP servers built with mcpkit.
package testutil

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TestClient wraps a client.Client with testing.T error handling and
// an in-process httptest.Server. Methods call t.Fatal on errors.
type TestClient struct {
	*client.Client
	t      *testing.T
	Server *httptest.Server
}

// NewTestClient creates a TestClient from a [server.Server].
// It starts an httptest.Server (Streamable HTTP), performs the MCP
// initialize handshake, and registers cleanup.
// Optional ClientOption values (e.g., client.WithClientBearerToken) are passed to the client.
//
// For a server with standard test fixtures, use [NewTestServer]:
//
//	tc := testutil.NewTestClient(t, testutil.NewTestServer())
func NewTestClient(t *testing.T, srv *server.Server, opts ...client.ClientOption) *TestClient {
	t.Helper()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{
		Name:    "test-client",
		Version: "0.0.1",
	}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("MCP connect failed: %v", err)
	}

	return &TestClient{Client: c, t: t, Server: ts}
}

// ToolCall invokes a tool and returns the first text content.
// Calls t.Fatal on error.
func (tc *TestClient) ToolCall(name string, args any) string {
	tc.t.Helper()
	text, err := tc.Client.ToolCall(name, args)
	if err != nil {
		tc.t.Fatalf("ToolCall(%s): %v", name, err)
	}
	return text
}

// ReadResource reads a resource URI and returns the text content.
// Calls t.Fatal on error.
func (tc *TestClient) ReadResource(uri string) string {
	tc.t.Helper()
	text, err := tc.Client.ReadResource(uri)
	if err != nil {
		tc.t.Fatalf("ReadResource(%s): %v", uri, err)
	}
	return text
}

// ListTools returns all registered tools. Calls t.Fatal on error.
func (tc *TestClient) ListTools() []core.ToolDef {
	tc.t.Helper()
	tools, err := tc.Client.ListTools()
	if err != nil {
		tc.t.Fatalf("ListTools: %v", err)
	}
	return tools
}

// ListResources returns all registered static resources. Calls t.Fatal on error.
func (tc *TestClient) ListResources() []core.ResourceDef {
	tc.t.Helper()
	resources, err := tc.Client.ListResources()
	if err != nil {
		tc.t.Fatalf("ListResources: %v", err)
	}
	return resources
}

// ListResourceTemplates returns all registered resource templates. Calls t.Fatal on error.
func (tc *TestClient) ListResourceTemplates() []core.ResourceTemplate {
	tc.t.Helper()
	templates, err := tc.Client.ListResourceTemplates()
	if err != nil {
		tc.t.Fatalf("ListResourceTemplates: %v", err)
	}
	return templates
}

// SubscribeResource subscribes to change notifications for a resource URI.
// Calls t.Fatal on error.
func (tc *TestClient) SubscribeResource(uri string) {
	tc.t.Helper()
	if err := tc.Client.SubscribeResource(uri); err != nil {
		tc.t.Fatalf("SubscribeResource(%s): %v", uri, err)
	}
}

// UnsubscribeResource removes a subscription for a resource URI.
// Calls t.Fatal on error.
func (tc *TestClient) UnsubscribeResource(uri string) {
	tc.t.Helper()
	if err := tc.Client.UnsubscribeResource(uri); err != nil {
		tc.t.Fatalf("UnsubscribeResource(%s): %v", uri, err)
	}
}

// Call makes a raw JSON-RPC call. Calls t.Fatal on error.
func (tc *TestClient) Call(method string, params any) *client.CallResult {
	tc.t.Helper()
	result, err := tc.Client.Call(method, params)
	if err != nil {
		tc.t.Fatalf("Call(%s): %v", method, err)
	}
	return result
}

package testutil_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

// newTestServer creates a minimal MCP server with an echo tool and
// a static resource for testing the TestClient wrapper.
func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "testutil-server", Version: "0.1.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "greet",
			Description: "Returns a greeting",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var p struct {
				Name string `json:"name"`
			}
			req.Bind(&p)
			return core.TextResult(fmt.Sprintf("hello %s", p.Name)), nil
		},
	)

	srv.RegisterResource(
		core.ResourceDef{URI: "test://data", Name: "Test Data", MimeType: "text/plain"},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{URI: "test://data", Text: "test data"}},
			}, nil
		},
	)

	return srv
}

// TestTestClientToolCall verifies that the TestClient.ToolCall convenience
// method correctly invokes a tool and returns the text result without
// requiring manual error handling.
func TestTestClientToolCall(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	text := c.ToolCall("greet", map[string]string{"name": "world"})
	if text != "hello world" {
		t.Errorf("ToolCall = %q, want 'hello world'", text)
	}
}

// TestTestClientReadResource verifies that the TestClient.ReadResource
// convenience method reads a static resource and returns its content.
func TestTestClientReadResource(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	text := c.ReadResource("test://data")
	if text != "test data" {
		t.Errorf("ReadResource = %q, want 'test data'", text)
	}
}

// TestTestClientListTools verifies that ListTools returns all registered
// tool definitions via the TestClient wrapper.
func TestTestClientListTools(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	tools := c.ListTools()
	if len(tools) != 1 || tools[0].Name != "greet" {
		t.Errorf("tools = %v, want [greet]", tools)
	}
}

// TestTestClientListResources verifies that ListResources returns all
// registered static resource definitions.
func TestTestClientListResources(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	resources := c.ListResources()
	if len(resources) != 1 || resources[0].URI != "test://data" {
		t.Errorf("resources = %v, want [test://data]", resources)
	}
}

// TestTestClientServerInfo verifies that the TestClient captures
// server info from the initialize handshake.
func TestTestClientServerInfo(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	if c.ServerInfo.Name != "testutil-server" {
		t.Errorf("server name = %q, want 'testutil-server'", c.ServerInfo.Name)
	}
}

// TestTestClientSessionID verifies that the TestClient has a valid
// session ID after connection.
func TestTestClientSessionID(t *testing.T) {
	srv := newTestServer(t)
	c := testutil.NewTestClient(t, srv)

	if c.SessionID() == "" {
		t.Error("no session ID")
	}
	if !strings.ContainsAny(c.SessionID(), "0123456789abcdef") {
		t.Errorf("session ID doesn't look like hex: %s", c.SessionID())
	}
}

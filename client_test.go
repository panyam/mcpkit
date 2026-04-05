package mcpkit

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// setupTestServer creates a mcpkit Server with sample tools and resources
// and returns a Client connected to it via httptest.
func setupTestServer(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()

	srv := NewServer(ServerInfo{Name: "test-server", Version: "1.0.0"})

	// Register a test tool
	srv.RegisterTool(
		ToolDef{
			Name:        "echo",
			Description: "Echoes the input message back",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"message": map[string]any{"type": "string"}},
				"required":   []string{"message"},
			},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			var p struct {
				Message string `json:"message"`
			}
			req.Bind(&p)
			return TextResult(fmt.Sprintf("echo: %s", p.Message)), nil
		},
	)

	// Register a tool that returns an error
	srv.RegisterTool(
		ToolDef{Name: "fail", Description: "Always fails"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return ErrorResult("intentional failure"), nil
		},
	)

	// Register a static resource
	srv.RegisterResource(
		ResourceDef{
			URI:         "test://info",
			Name:        "Test Info",
			Description: "Static test resource",
			MimeType:    "text/plain",
		},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{
				Contents: []ResourceReadContent{{
					URI:      "test://info",
					MimeType: "text/plain",
					Text:     "hello from test",
				}},
			}, nil
		},
	)

	// Register a resource template
	srv.RegisterResourceTemplate(
		ResourceTemplate{
			URITemplate: "test://items/{id}",
			Name:        "Test Item",
			Description: "Parameterized test resource",
			MimeType:    "text/plain",
		},
		func(ctx context.Context, uri string, params map[string]string) (ResourceResult, error) {
			return ResourceResult{
				Contents: []ResourceReadContent{{
					URI:      uri,
					MimeType: "text/plain",
					Text:     fmt.Sprintf("item %s", params["id"]),
				}},
			}, nil
		},
	)

	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	return c, ts
}

// TestClientConnect verifies that the client performs the MCP initialize
// handshake, obtains a session ID, and captures the server info.
func TestClientConnect(t *testing.T) {
	c, _ := setupTestServer(t)

	if c.SessionID() == "" {
		t.Error("no session ID after connect")
	}
	if c.ServerInfo.Name != "test-server" {
		t.Errorf("server name = %q, want test-server", c.ServerInfo.Name)
	}
	if c.ServerInfo.Version != "1.0.0" {
		t.Errorf("server version = %q, want 1.0.0", c.ServerInfo.Version)
	}
}

// TestClientToolCall verifies that ToolCall invokes a tool and returns
// the first text content from the response.
func TestClientToolCall(t *testing.T) {
	c, _ := setupTestServer(t)

	text, err := c.ToolCall("echo", map[string]string{"message": "world"})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text != "echo: world" {
		t.Errorf("ToolCall result = %q, want 'echo: world'", text)
	}
}

// TestClientToolCallError verifies that ToolCall returns an error when
// the tool reports isError:true in its response.
func TestClientToolCallError(t *testing.T) {
	c, _ := setupTestServer(t)

	_, err := c.ToolCall("fail", nil)
	if err == nil {
		t.Fatal("expected error from fail tool")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Errorf("error = %v, want 'intentional failure'", err)
	}
}

// TestClientReadResource verifies that ReadResource reads a static resource
// and returns its text content.
func TestClientReadResource(t *testing.T) {
	c, _ := setupTestServer(t)

	text, err := c.ReadResource("test://info")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if text != "hello from test" {
		t.Errorf("resource text = %q, want 'hello from test'", text)
	}
}

// TestClientReadResourceTemplate verifies that ReadResource resolves a URI
// template and returns the parameterized content.
func TestClientReadResourceTemplate(t *testing.T) {
	c, _ := setupTestServer(t)

	text, err := c.ReadResource("test://items/42")
	if err != nil {
		t.Fatalf("ReadResource template: %v", err)
	}
	if text != "item 42" {
		t.Errorf("resource text = %q, want 'item 42'", text)
	}
}

// TestClientListTools verifies that ListTools returns all registered tool
// definitions with correct names.
func TestClientListTools(t *testing.T) {
	c, _ := setupTestServer(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["echo"] {
		t.Error("missing tool: echo")
	}
	if !names["fail"] {
		t.Error("missing tool: fail")
	}
}

// TestClientListResources verifies that ListResources returns all registered
// static resource definitions.
func TestClientListResources(t *testing.T) {
	c, _ := setupTestServer(t)

	resources, err := c.ListResources()
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 || resources[0].URI != "test://info" {
		t.Errorf("resources = %v, want [test://info]", resources)
	}
}

// TestClientListResourceTemplates verifies that ListResourceTemplates returns
// all registered resource template definitions.
func TestClientListResourceTemplates(t *testing.T) {
	c, _ := setupTestServer(t)

	templates, err := c.ListResourceTemplates()
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(templates) != 1 || templates[0].URITemplate != "test://items/{id}" {
		t.Errorf("templates = %v, want [test://items/{id}]", templates)
	}
}

// TestClientCallRaw verifies that the low-level Call method returns a
// CallResult that can be unmarshalled into typed structs.
func TestClientCallRaw(t *testing.T) {
	c, _ := setupTestServer(t)

	result, err := c.Call("tools/list", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	var resp struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(resp.Tools) < 2 {
		t.Errorf("expected >=2 tools, got %d", len(resp.Tools))
	}
}

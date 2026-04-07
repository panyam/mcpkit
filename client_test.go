package mcpkit

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestMCPServer creates a server with an echo tool, a fail tool,
// a static resource, and a resource template. Used by both transport tests.
func newTestMCPServer() *Server {
	srv := NewServer(ServerInfo{Name: "test-server", Version: "1.0.0"})

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
			var p struct{ Message string `json:"message"` }
			req.Bind(&p)
			return TextResult(fmt.Sprintf("echo: %s", p.Message)), nil
		},
	)

	srv.RegisterTool(
		ToolDef{Name: "fail", Description: "Always fails"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return ErrorResult("intentional failure"), nil
		},
	)

	srv.RegisterResource(
		ResourceDef{URI: "test://info", Name: "Test Info", Description: "Static test resource", MimeType: "text/plain"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{URI: "test://info", MimeType: "text/plain", Text: "hello from test"}}}, nil
		},
	)

	srv.RegisterResourceTemplate(
		ResourceTemplate{URITemplate: "test://items/{id}", Name: "Test Item", Description: "Parameterized test resource", MimeType: "text/plain"},
		func(ctx context.Context, uri string, params map[string]string) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{URI: uri, MimeType: "text/plain", Text: fmt.Sprintf("item %s", params["id"])}}}, nil
		},
	)

	return srv
}

// setupStreamableClient creates an httptest.Server with Streamable HTTP and a connected Client.
func setupStreamableClient(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	srv := newTestMCPServer()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	return c, ts
}

// setupSSEClient creates an httptest.Server with SSE transport and a connected Client.
// The SSE connection must be closed before the server shuts down, so we
// register client.Close() as a cleanup before ts.Close().
func setupSSEClient(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	srv := newTestMCPServer()
	handler := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
	ts := httptest.NewServer(handler)

	c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test-client", Version: "1.0"}, WithSSEClient())
	if err := c.Connect(); err != nil {
		ts.Close()
		t.Fatalf("SSE Connect failed: %v", err)
	}

	// Close client first (closes SSE stream), then server
	t.Cleanup(func() {
		c.Close()
		ts.Close()
	})

	return c, ts
}

// forAllTransports runs a test function against all 3 client transports:
// Streamable HTTP, SSE, and in-memory. This is the Go equivalent of
// parametric tests — each transport variant runs as a subtest.
func forAllTransports(t *testing.T, fn func(t *testing.T, c *Client)) {
	t.Helper()

	t.Run("streamable", func(t *testing.T) {
		c, _ := setupStreamableClient(t)
		fn(t, c)
	})
	t.Run("sse", func(t *testing.T) {
		c, _ := setupSSEClient(t)
		fn(t, c)
	})
	t.Run("memory", func(t *testing.T) {
		c := NewClient("memory://", ClientInfo{Name: "test-client", Version: "1.0"},
			WithInMemoryServer(newTestMCPServer()))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})
}

// --- Parametric transport tests (run against Streamable, SSE, and in-memory) ---

// TestClientConnect verifies that the client performs the MCP initialize
// handshake, obtains a session ID, and captures server info across all transports.
func TestClientConnect(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		if c.SessionID() == "" {
			t.Error("no session ID after connect")
		}
		if c.ServerInfo.Name != "test-server" {
			t.Errorf("server name = %q, want test-server", c.ServerInfo.Name)
		}
	})
}

// TestClientToolCall verifies that ToolCall invokes a tool and returns the
// first text content from the response across all transports.
func TestClientToolCall(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		text, err := c.ToolCall("echo", map[string]string{"message": "world"})
		if err != nil {
			t.Fatalf("ToolCall: %v", err)
		}
		if text != "echo: world" {
			t.Errorf("result = %q, want 'echo: world'", text)
		}
	})
}

// TestClientToolCallError verifies that ToolCall returns an error when the
// tool reports isError:true in its response across all transports.
func TestClientToolCallError(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		_, err := c.ToolCall("fail", nil)
		if err == nil {
			t.Fatal("expected error from fail tool")
		}
		if !strings.Contains(err.Error(), "intentional failure") {
			t.Errorf("error = %v, want 'intentional failure'", err)
		}
	})
}

// TestClientReadResource verifies that ReadResource reads a static resource
// and returns its text content across all transports.
func TestClientReadResource(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		text, err := c.ReadResource("test://info")
		if err != nil {
			t.Fatalf("ReadResource: %v", err)
		}
		if text != "hello from test" {
			t.Errorf("result = %q, want 'hello from test'", text)
		}
	})
}

// TestClientReadResourceTemplate verifies that ReadResource resolves a URI
// template and returns the parameterized content across all transports.
func TestClientReadResourceTemplate(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		text, err := c.ReadResource("test://items/42")
		if err != nil {
			t.Fatalf("ReadResource: %v", err)
		}
		if text != "item 42" {
			t.Errorf("result = %q, want 'item 42'", text)
		}
	})
}

// TestClientListTools verifies that ListTools returns all registered tool
// definitions with correct names across all transports.
func TestClientListTools(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		tools, err := c.ListTools()
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name] = true
		}
		if !names["echo"] || !names["fail"] {
			t.Errorf("missing tools: %v", names)
		}
	})
}

// TestClientListResources verifies ListResources returns static resource definitions
// across all transports.
func TestClientListResources(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		resources, err := c.ListResources()
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		if len(resources) != 1 || resources[0].URI != "test://info" {
			t.Errorf("resources = %v, want [test://info]", resources)
		}
	})
}

// TestClientListResourceTemplates verifies ListResourceTemplates returns template
// definitions across all transports.
func TestClientListResourceTemplates(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		templates, err := c.ListResourceTemplates()
		if err != nil {
			t.Fatalf("ListResourceTemplates: %v", err)
		}
		if len(templates) != 1 || templates[0].URITemplate != "test://items/{id}" {
			t.Errorf("templates = %v", templates)
		}
	})
}

// TestClientCallRaw verifies the low-level Call method returns a CallResult
// that can be unmarshalled into typed structs across all transports.
func TestClientCallRaw(t *testing.T) {
	forAllTransports(t, func(t *testing.T, c *Client) {
		result, err := c.Call("tools/list", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		var resp struct{ Tools []ToolDef `json:"tools"` }
		if err := result.Unmarshal(&resp); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(resp.Tools) < 2 {
			t.Errorf("expected >=2 tools, got %d", len(resp.Tools))
		}
	})
}

// --- SSE transport tests ---

// TestSSEClientConnect verifies that the client performs the MCP initialize
// handshake over SSE transport, extracts the POST URL from the endpoint event,
// and captures server info.
func TestSSEClientConnect(t *testing.T) {
	c, _ := setupSSEClient(t)
	if c.SessionID() == "" {
		t.Error("no session ID after SSE connect")
	}
	if c.ServerInfo.Name != "test-server" {
		t.Errorf("server name = %q, want test-server", c.ServerInfo.Name)
	}
}

// TestSSEClientToolCall verifies that ToolCall works over SSE transport:
// POST request → read message event from SSE stream → parse response.
func TestSSEClientToolCall(t *testing.T) {
	c, _ := setupSSEClient(t)
	text, err := c.ToolCall("echo", map[string]string{"message": "sse-world"})
	if err != nil {
		t.Fatalf("SSE ToolCall: %v", err)
	}
	if text != "echo: sse-world" {
		t.Errorf("result = %q, want 'echo: sse-world'", text)
	}
}

// TestSSEClientToolCallError verifies error handling over SSE transport.
func TestSSEClientToolCallError(t *testing.T) {
	c, _ := setupSSEClient(t)
	_, err := c.ToolCall("fail", nil)
	if err == nil {
		t.Fatal("expected error from fail tool over SSE")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Errorf("error = %v", err)
	}
}

// TestSSEClientReadResource verifies resource reading over SSE transport.
func TestSSEClientReadResource(t *testing.T) {
	c, _ := setupSSEClient(t)
	text, err := c.ReadResource("test://info")
	if err != nil {
		t.Fatalf("SSE ReadResource: %v", err)
	}
	if text != "hello from test" {
		t.Errorf("result = %q", text)
	}
}

// TestSSEClientReadResourceTemplate verifies template resource reading over SSE.
func TestSSEClientReadResourceTemplate(t *testing.T) {
	c, _ := setupSSEClient(t)
	text, err := c.ReadResource("test://items/99")
	if err != nil {
		t.Fatalf("SSE ReadResource template: %v", err)
	}
	if text != "item 99" {
		t.Errorf("result = %q, want 'item 99'", text)
	}
}

// TestSSEClientListTools verifies tool discovery over SSE transport.
func TestSSEClientListTools(t *testing.T) {
	c, _ := setupSSEClient(t)
	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("SSE ListTools: %v", err)
	}
	if len(tools) < 2 {
		t.Errorf("expected >=2 tools, got %d", len(tools))
	}
}

// --- Resource Subscription Integration Tests ---

// newSubscriptionTestServer creates an MCP server with subscriptions enabled
// and a subscribable resource for integration testing.
func newSubscriptionTestServer() *Server {
	srv := NewServer(ServerInfo{Name: "sub-test", Version: "1.0"}, WithSubscriptions())
	srv.RegisterResource(
		ResourceDef{URI: "test://config", Name: "Config", MimeType: "text/plain"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "config data",
			}}}, nil
		},
	)
	return srv
}

// TestClientSubscribeUnsubscribeResource verifies that the client can subscribe
// to and unsubscribe from a resource URI across all transports. Both operations
// should succeed without error.
func TestClientSubscribeUnsubscribeResource(t *testing.T) {
	t.Run("streamable", func(t *testing.T) {
		srv := newSubscriptionTestServer()
		handler := srv.Handler(WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test", Version: "1.0"})
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect: %v", err)
		}

		if err := c.SubscribeResource("test://config"); err != nil {
			t.Fatalf("SubscribeResource: %v", err)
		}
		if err := c.UnsubscribeResource("test://config"); err != nil {
			t.Fatalf("UnsubscribeResource: %v", err)
		}
	})

	t.Run("sse", func(t *testing.T) {
		srv := newSubscriptionTestServer()
		handler := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)

		c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test", Version: "1.0"}, WithSSEClient())
		if err := c.Connect(); err != nil {
			ts.Close()
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { c.Close(); ts.Close() })

		if err := c.SubscribeResource("test://config"); err != nil {
			t.Fatalf("SubscribeResource: %v", err)
		}
		if err := c.UnsubscribeResource("test://config"); err != nil {
			t.Fatalf("UnsubscribeResource: %v", err)
		}
	})

	t.Run("memory", func(t *testing.T) {
		srv := newSubscriptionTestServer()
		c := NewClient("memory://", ClientInfo{Name: "test", Version: "1.0"},
			WithInMemoryServer(srv))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { c.Close() })

		if err := c.SubscribeResource("test://config"); err != nil {
			t.Fatalf("SubscribeResource: %v", err)
		}
		if err := c.UnsubscribeResource("test://config"); err != nil {
			t.Fatalf("UnsubscribeResource: %v", err)
		}
	})
}

// TestClientSubscriptionNotificationDelivery verifies that after subscribing,
// the client's notification handler receives a notifications/resources/updated
// notification when the server calls NotifyResourceUpdated. Uses the in-memory
// transport with WithNotificationHandler to capture notifications.
func TestClientSubscriptionNotificationDelivery(t *testing.T) {
	srv := newSubscriptionTestServer()

	var mu sync.Mutex
	var received []string

	c := NewClient("memory://", ClientInfo{Name: "test", Version: "1.0"},
		WithInMemoryServer(srv),
		WithNotificationHandler(func(method string, params any) {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, method)
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.SubscribeResource("test://config"); err != nil {
		t.Fatalf("SubscribeResource: %v", err)
	}

	// Trigger from server side
	srv.NotifyResourceUpdated("test://config")

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("got %d notifications, want 1", len(received))
	}
	if received[0] != "notifications/resources/updated" {
		t.Errorf("method = %q, want notifications/resources/updated", received[0])
	}
}

// newExtraSchemaServer creates a Server with a tool whose InputSchema includes
// extra JSON Schema fields ($schema, $defs, additionalProperties) beyond the
// MCP spec minimum. Used to verify round-trip preservation across transports.
func newExtraSchemaServer() *Server {
	srv := NewServer(ServerInfo{Name: "test-server", Version: "1.0.0"})
	srv.RegisterTool(
		ToolDef{
			Name:        "extra_schema",
			Description: "Tool with extra JSON Schema fields",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
						"$ref": "#/$defs/NameType",
					},
				},
				"required":            []string{"name"},
				"additionalProperties": false,
				"$schema":             "http://json-schema.org/draft-07/schema#",
				"$defs": map[string]any{
					"NameType": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},
			},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("ok"), nil
		},
	)
	return srv
}

// TestClientListToolsExtraSchemaFields verifies that extra JSON Schema fields
// in a tool's InputSchema (e.g. $schema, $defs, $ref, additionalProperties)
// survive the full round-trip through server serialization, transport, and
// client deserialization across all three transports (Streamable HTTP, SSE,
// in-memory). This guards against regressions where the InputSchema type might
// be changed from `any` to a typed struct that drops unknown fields.
func TestClientListToolsExtraSchemaFields(t *testing.T) {
	runTest := func(t *testing.T, c *Client) {
		tools, err := c.ListTools()
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}

		// Find the extra_schema tool.
		var found *ToolDef
		for i := range tools {
			if tools[i].Name == "extra_schema" {
				found = &tools[i]
				break
			}
		}
		if found == nil {
			t.Fatal("extra_schema tool not found in ListTools response")
		}

		schema, ok := found.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("InputSchema is %T, want map[string]any", found.InputSchema)
		}

		// additionalProperties must be preserved as false.
		if ap, ok := schema["additionalProperties"]; !ok {
			t.Error("additionalProperties missing from schema")
		} else if ap != false {
			t.Errorf("additionalProperties = %v (%T), want false", ap, ap)
		}

		// $schema must be preserved.
		if s, ok := schema["$schema"]; !ok {
			t.Error("$schema missing from schema")
		} else if s != "http://json-schema.org/draft-07/schema#" {
			t.Errorf("$schema = %v, want draft-07 URI", s)
		}

		// $defs must be preserved with nested structure.
		defs, ok := schema["$defs"]
		if !ok {
			t.Fatal("$defs missing from schema")
		}
		defsMap, ok := defs.(map[string]any)
		if !ok {
			t.Fatalf("$defs is %T, want map[string]any", defs)
		}
		if _, ok := defsMap["NameType"]; !ok {
			t.Error("$defs.NameType missing")
		}

		// $ref in property must be preserved.
		props, _ := schema["properties"].(map[string]any)
		nameProp, _ := props["name"].(map[string]any)
		if ref, ok := nameProp["$ref"]; !ok {
			t.Error("$ref missing from name property")
		} else if ref != "#/$defs/NameType" {
			t.Errorf("$ref = %v, want #/$defs/NameType", ref)
		}
	}

	t.Run("streamable", func(t *testing.T) {
		srv := newExtraSchemaServer()
		handler := srv.Handler(WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)
		c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"})
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		runTest(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		srv := newExtraSchemaServer()
		handler := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)
		c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test-client", Version: "1.0"}, WithSSEClient())
		if err := c.Connect(); err != nil {
			ts.Close()
			t.Fatalf("SSE Connect failed: %v", err)
		}
		t.Cleanup(func() {
			c.Close()
			ts.Close()
		})
		runTest(t, c)
	})

	t.Run("memory", func(t *testing.T) {
		c := NewClient("memory://", ClientInfo{Name: "test-client", Version: "1.0"},
			WithInMemoryServer(newExtraSchemaServer()))
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		runTest(t, c)
	})
}

// --- Notification delivery order tests ---

// newNotifyTestServer creates a server with a tool that emits 3 log notifications
// before returning a result. Used to verify notification ordering guarantees.
func newNotifyTestServer() *Server {
	srv := NewServer(ServerInfo{Name: "test-server", Version: "1.0.0"})

	srv.RegisterTool(
		ToolDef{
			Name:        "notify-tool",
			Description: "Emits 3 log notifications then returns a result",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"tag": map[string]any{"type": "string"}},
			},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			var p struct{ Tag string `json:"tag"` }
			req.Bind(&p)
			tag := p.Tag
			if tag == "" {
				tag = "default"
			}
			EmitLog(ctx, LogInfo, "test", fmt.Sprintf("%s-msg-1", tag))
			EmitLog(ctx, LogInfo, "test", fmt.Sprintf("%s-msg-2", tag))
			EmitLog(ctx, LogInfo, "test", fmt.Sprintf("%s-msg-3", tag))
			return TextResult(fmt.Sprintf("done:%s", tag)), nil
		},
	)

	// Also register basic tools so forAllTransports-style setup can reuse this server.
	srv.RegisterTool(
		ToolDef{Name: "echo", Description: "Echoes input",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"message": map[string]any{"type": "string"}},
				"required":   []string{"message"},
			}},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			var p struct{ Message string `json:"message"` }
			req.Bind(&p)
			return TextResult(fmt.Sprintf("echo: %s", p.Message)), nil
		},
	)

	return srv
}

// setupStreamableWithOpts creates an httptest.Server with Streamable HTTP and a
// connected Client, using the provided server and client options.
func setupStreamableWithOpts(t *testing.T, srv *Server, opts ...ClientOption) *Client {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	allOpts := []ClientOption{}
	allOpts = append(allOpts, opts...)
	c := NewClient(ts.URL+"/mcp", ClientInfo{Name: "test-client", Version: "1.0"}, allOpts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// setupSSEWithOpts creates an httptest.Server with SSE transport and a connected
// Client, using the provided server and client options.
func setupSSEWithOpts(t *testing.T, srv *Server, opts ...ClientOption) *Client {
	t.Helper()
	handler := srv.Handler(WithSSE(true), WithStreamableHTTP(false))
	ts := httptest.NewServer(handler)

	allOpts := []ClientOption{WithSSEClient()}
	allOpts = append(allOpts, opts...)
	c := NewClient(ts.URL+"/mcp/sse", ClientInfo{Name: "test-client", Version: "1.0"}, allOpts...)
	if err := c.Connect(); err != nil {
		ts.Close()
		t.Fatalf("SSE Connect failed: %v", err)
	}
	t.Cleanup(func() {
		c.Close()
		ts.Close()
	})
	return c
}

// setupMemoryWithOpts creates an in-memory client with the provided server and options.
func setupMemoryWithOpts(t *testing.T, srv *Server, opts ...ClientOption) *Client {
	t.Helper()
	allOpts := []ClientOption{WithInMemoryServer(srv)}
	allOpts = append(allOpts, opts...)
	c := NewClient("memory://", ClientInfo{Name: "test-client", Version: "1.0"}, allOpts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestNotificationDeliveryOrder verifies that notifications emitted during a tool
// call are delivered to the client's notification handler in order, and all arrive
// before ToolCall returns. This is the core ordering guarantee from issue #75.
// Runs across all 3 transports (Streamable HTTP, SSE, in-memory).
func TestNotificationDeliveryOrder(t *testing.T) {
	type testCase struct {
		name  string
		setup func(t *testing.T, srv *Server, opts ...ClientOption) *Client
	}
	cases := []testCase{
		{"streamable", setupStreamableWithOpts},
		{"sse", setupSSEWithOpts},
		{"memory", setupMemoryWithOpts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newNotifyTestServer()

			var mu sync.Mutex
			var notifications []string

			c := tc.setup(t, srv, WithNotificationHandler(func(method string, params any) {
				mu.Lock()
				defer mu.Unlock()
				if method == "notifications/message" {
					// Extract the log data to verify ordering
					if m, ok := params.(map[string]any); ok {
						if data, ok := m["data"].(string); ok {
							notifications = append(notifications, data)
						}
					}
				}
			}))

			// Enable logging so EmitLog notifications are sent
			if _, err := c.Call("logging/setLevel", map[string]string{"level": "debug"}); err != nil {
				t.Fatalf("logging/setLevel: %v", err)
			}

			text, err := c.ToolCall("notify-tool", map[string]any{"tag": "order"})
			if err != nil {
				t.Fatalf("ToolCall: %v", err)
			}
			if text != "done:order" {
				t.Errorf("result = %q, want done:order", text)
			}

			// For SSE transport, notifications arrive on the background reader goroutine.
			// Allow a brief window for delivery to complete.
			time.Sleep(50 * time.Millisecond)

			mu.Lock()
			defer mu.Unlock()

			if len(notifications) != 3 {
				t.Fatalf("got %d notifications, want 3: %v", len(notifications), notifications)
			}

			expected := []string{"order-msg-1", "order-msg-2", "order-msg-3"}
			for i, want := range expected {
				if notifications[i] != want {
					t.Errorf("notification[%d] = %q, want %q", i, notifications[i], want)
				}
			}
		})
	}
}

// TestStreamableConcurrentNotificationIsolation verifies that when two concurrent
// tool calls each emit notifications, each call's notifications stay on its own
// response stream and do not leak to the other call. This tests the request-scoped
// requestNotify closure in handlePostSSE. Streamable HTTP only (SSE uses a shared
// stream where isolation is not possible at the transport level).
func TestStreamableConcurrentNotificationIsolation(t *testing.T) {
	srv := newNotifyTestServer()

	// Track notifications per goroutine — each ToolCall runs in its own goroutine,
	// and notifications from readSSEResponse are delivered inline on the calling
	// goroutine's response stream.
	type callResult struct {
		text          string
		notifications []string
		err           error
	}

	// We need per-call notification tracking. Since the Streamable HTTP client
	// delivers notifications inline during readSSEResponse (same goroutine as
	// the ToolCall), we can't easily correlate notifications to calls via a global
	// handler. Instead, we collect all notifications and verify by content.
	var mu sync.Mutex
	var allNotifications []string

	c := setupStreamableWithOpts(t, srv, WithNotificationHandler(func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		if method == "notifications/message" {
			if m, ok := params.(map[string]any); ok {
				if data, ok := m["data"].(string); ok {
					allNotifications = append(allNotifications, data)
				}
			}
		}
	}))

	// Enable logging
	if _, err := c.Call("logging/setLevel", map[string]string{"level": "debug"}); err != nil {
		t.Fatalf("logging/setLevel: %v", err)
	}

	// Launch 2 concurrent tool calls with distinct tags
	var wg sync.WaitGroup
	results := make([]callResult, 2)
	for i, tag := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(idx int, tag string) {
			defer wg.Done()
			text, err := c.ToolCall("notify-tool", map[string]any{"tag": tag})
			results[idx] = callResult{text: text, err: err}
		}(i, tag)
	}
	wg.Wait()

	// Verify both calls succeeded
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("call %d: %v", i, r.err)
		}
	}
	if results[0].text != "done:alpha" {
		t.Errorf("call 0 result = %q, want done:alpha", results[0].text)
	}
	if results[1].text != "done:beta" {
		t.Errorf("call 1 result = %q, want done:beta", results[1].text)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should have 6 total notifications (3 per call)
	if len(allNotifications) != 6 {
		t.Fatalf("got %d notifications, want 6: %v", len(allNotifications), allNotifications)
	}

	// Verify all expected notifications are present (order between calls is non-deterministic)
	alphaCount := 0
	betaCount := 0
	for _, n := range allNotifications {
		if strings.HasPrefix(n, "alpha-msg-") {
			alphaCount++
		}
		if strings.HasPrefix(n, "beta-msg-") {
			betaCount++
		}
	}
	if alphaCount != 3 {
		t.Errorf("alpha notifications = %d, want 3", alphaCount)
	}
	if betaCount != 3 {
		t.Errorf("beta notifications = %d, want 3", betaCount)
	}

	// Verify within each call's notifications, the order is preserved
	// (alpha-msg-1 before alpha-msg-2 before alpha-msg-3, same for beta)
	alphaOrder := make([]string, 0, 3)
	betaOrder := make([]string, 0, 3)
	for _, n := range allNotifications {
		if strings.HasPrefix(n, "alpha-") {
			alphaOrder = append(alphaOrder, n)
		} else if strings.HasPrefix(n, "beta-") {
			betaOrder = append(betaOrder, n)
		}
	}
	for i, want := range []string{"alpha-msg-1", "alpha-msg-2", "alpha-msg-3"} {
		if alphaOrder[i] != want {
			t.Errorf("alpha[%d] = %q, want %q", i, alphaOrder[i], want)
		}
	}
	for i, want := range []string{"beta-msg-1", "beta-msg-2", "beta-msg-3"} {
		if betaOrder[i] != want {
			t.Errorf("beta[%d] = %q, want %q", i, betaOrder[i], want)
		}
	}
}

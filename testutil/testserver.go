package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// NewTestServer creates an MCP server pre-registered with standard test
// fixtures for use across unit and integration tests.
//
// Standard fixtures:
//   - Tool "echo": echoes input message → "echo: <message>"
//   - Tool "fail": always returns an error result ("intentional failure")
//   - Resource "test://info": static text → "hello from test"
//   - ResourceTemplate "test://items/{id}": parameterized → "item <id>"
//
// Additional tools, resources, or prompts can be registered on the returned
// server before passing it to [NewTestClient] or [ForAllTransports].
//
// Example:
//
//	srv := testutil.NewTestServer()
//	srv.RegisterTool(myCustomTool, myHandler) // add test-specific tools
//	tc := testutil.NewTestClient(t, srv)
//	result := tc.ToolCall("echo", map[string]any{"message": "hi"})
func NewTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "test-server", Version: "1.0.0"})

	type echoInput struct {
		Message string `json:"message"`
	}
	srv.Register(core.TextTool[echoInput]("echo", "Echoes the input message back",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			return fmt.Sprintf("echo: %s", input.Message), nil
		},
	))

	srv.Register(core.TextTool[struct{}]("fail", "Always fails with an error result",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "", fmt.Errorf("intentional failure")
		},
	))

	srv.RegisterResource(
		core.ResourceDef{
			URI:         "test://info",
			Name:        "Test Info",
			Description: "Static test resource",
			MimeType:    "text/plain",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      "test://info",
					MimeType: "text/plain",
					Text:     "hello from test",
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "test://items/{id}",
			Name:        "Test Item",
			Description: "Parameterized test resource",
			MimeType:    "text/plain",
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      uri,
					MimeType: "text/plain",
					Text:     fmt.Sprintf("item %s", params["id"]),
				}},
			}, nil
		},
	)

	return srv
}

// InitHandshake performs the MCP initialize + notifications/initialized
// handshake on any type that implements the Dispatch method (both
// [server.Server] and [server.Dispatcher] satisfy this).
//
// This is required before the server will accept tool/resource requests.
// For client-based tests, prefer [NewTestClient] which handles the handshake
// automatically via client.Connect().
//
// Example:
//
//	srv := server.NewServer(info)
//	srv.RegisterTool(...)
//	testutil.InitHandshake(srv)
//	resp := srv.Dispatch(ctx, toolCallRequest)
func InitHandshake(d interface {
	Dispatch(context.Context, *core.Request) (*core.Response, error)
}) {
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
}

// ToolCallRequest builds a JSON-RPC tools/call request for direct dispatch testing.
func ToolCallRequest(name string, args map[string]any) *core.Request {
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	raw, _ := json.Marshal(params)
	return &core.Request{JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "tools/call", Params: raw}
}

// ResourceReadRequest builds a JSON-RPC resources/read request for direct dispatch testing.
func ResourceReadRequest(uri string) *core.Request {
	raw, _ := json.Marshal(map[string]string{"uri": uri})
	return &core.Request{JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "resources/read", Params: raw}
}

// PromptGetRequest builds a JSON-RPC prompts/get request for direct dispatch testing.
func PromptGetRequest(name string, args map[string]string) *core.Request {
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	raw, _ := json.Marshal(params)
	return &core.Request{JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "prompts/get", Params: raw}
}

// ForAllTransports runs fn as a subtest against all 4 MCP transport types:
// Streamable HTTP, SSE, in-process memory, and stdio. Each subtest creates
// its own server instance, connected client, and cleanup handlers.
//
// Use this for any test that should be transport-agnostic — the test logic
// runs identically regardless of the underlying transport.
//
// The server parameter is used as a template: ForAllTransports creates a fresh
// server for each transport by calling NewTestServer(). If you need the exact
// server instance passed in (e.g., with custom tools), note that Streamable
// HTTP and SSE subtests create httptest.Servers from it directly, while memory
// and stdio create fresh copies via NewTestServer() to avoid shared state.
//
// Example:
//
//	func TestEcho(t *testing.T) {
//	    testutil.ForAllTransports(t, testutil.NewTestServer(), func(t *testing.T, c *client.Client) {
//	        text, err := c.ToolCall("echo", map[string]any{"message": "hi"})
//	        if err != nil { t.Fatal(err) }
//	        assert.Equal(t, "echo: hi", text)
//	    })
//	}
func ForAllTransports(t *testing.T, srv *server.Server, fn func(t *testing.T, c *client.Client)) {
	t.Helper()

	t.Run("streamable", func(t *testing.T) {
		handler := srv.Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"})
		if err := c.Connect(); err != nil {
			t.Fatalf("Streamable Connect failed: %v", err)
		}
		fn(t, c)
	})

	t.Run("sse", func(t *testing.T) {
		handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)

		c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test-client", Version: "1.0"}, client.WithSSEClient())
		if err := c.Connect(); err != nil {
			ts.Close()
			t.Fatalf("SSE Connect failed: %v", err)
		}

		// Close client first (closes SSE stream), then server.
		t.Cleanup(func() {
			c.Close()
			ts.Close()
		})

		fn(t, c)
	})

	t.Run("memory", func(t *testing.T) {
		c := client.NewClient("memory://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithTransport(server.NewInProcessTransport(srv)))
		if err := c.Connect(); err != nil {
			t.Fatalf("Memory Connect failed: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		fn(t, c)
	})

	t.Run("stdio", func(t *testing.T) {
		// Two pipes: server reads what client writes, client reads what server writes.
		sr, cw := io.Pipe() // server reads from sr, client writes to cw
		cr, sw := io.Pipe() // client reads from cr, server writes to sw

		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			srv.RunStdio(ctx, server.WithStdioInput(sr), server.WithStdioOutput(sw))
			sw.Close()
		}()

		c := client.NewClient("stdio://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithStdioTransport(cr, cw))
		if err := c.Connect(); err != nil {
			cancel()
			t.Fatalf("Stdio Connect failed: %v", err)
		}

		t.Cleanup(func() {
			c.Close()
			cancel()
		})

		fn(t, c)
	})
}

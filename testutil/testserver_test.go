package testutil

import (
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// TestNewTestServer_HasEchoTool verifies that NewTestServer registers an "echo"
// tool that appears in tools/list. This is the most commonly used test fixture
// across client and server test suites.
func TestNewTestServer_HasEchoTool(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	tools := tc.ListTools()

	found := false
	for _, tool := range tools {
		if tool.Name == "echo" {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewTestServer should register an 'echo' tool")
	}
}

// TestNewTestServer_HasFailTool verifies that NewTestServer registers a "fail"
// tool that always returns an error result. Used to test error handling paths
// in client and server code.
func TestNewTestServer_HasFailTool(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	tools := tc.ListTools()

	found := false
	for _, tool := range tools {
		if tool.Name == "fail" {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewTestServer should register a 'fail' tool")
	}
}

// TestNewTestServer_HasResource verifies that NewTestServer registers a static
// resource at "test://info" that is discoverable via resources/list.
func TestNewTestServer_HasResource(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	resources := tc.ListResources()

	found := false
	for _, r := range resources {
		if r.URI == "test://info" {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewTestServer should register a 'test://info' resource")
	}
}

// TestNewTestServer_HasTemplate verifies that NewTestServer registers a
// parameterized resource template at "test://items/{id}" that is discoverable
// via resources/templates/list.
func TestNewTestServer_HasTemplate(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	templates := tc.ListResourceTemplates()

	found := false
	for _, tmpl := range templates {
		if tmpl.URITemplate == "test://items/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Error("NewTestServer should register a 'test://items/{id}' resource template")
	}
}

// TestNewTestServer_EchoWorks verifies the echo tool round-trip: connect to the
// server, call tools/call with a message, and confirm the response is
// "echo: <message>". This exercises the full MCP request path.
func TestNewTestServer_EchoWorks(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	result := tc.ToolCall("echo", map[string]any{"message": "hello"})
	if result != "echo: hello" {
		t.Errorf("echo tool returned %q, want %q", result, "echo: hello")
	}
}

// TestNewTestServer_FailReturnsError verifies the fail tool returns an error
// result (isError: true). This is the standard fixture for testing client-side
// error handling.
func TestNewTestServer_FailReturnsError(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	raw := tc.Call("tools/call", map[string]any{"name": "fail"})

	// The fail tool returns a ToolResult with IsError=true.
	// CallResult.Result contains the JSON-RPC response.
	var result core.ToolResult
	if err := raw.Unmarshal(&result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("fail tool should return isError: true")
	}
}

// TestNewTestServer_ResourceReadable verifies the static resource at
// "test://info" returns the expected text content via resources/read.
func TestNewTestServer_ResourceReadable(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	text := tc.ReadResource("test://info")
	if text != "hello from test" {
		t.Errorf("resource text = %q, want %q", text, "hello from test")
	}
}

// TestNewTestServer_TemplateReadable verifies the parameterized resource
// template at "test://items/{id}" resolves correctly. When read with id=42,
// it should return "item 42".
func TestNewTestServer_TemplateReadable(t *testing.T) {
	srv := NewTestServer()
	tc := NewTestClient(t, srv)
	text := tc.ReadResource("test://items/42")
	if text != "item 42" {
		t.Errorf("template text = %q, want %q", text, "item 42")
	}
}

// TestInitHandshake verifies that InitHandshake performs the MCP initialize +
// notifications/initialized handshake so subsequent tool calls succeed. Without
// the handshake, tool calls return "server not initialized" errors.
func TestInitHandshake(t *testing.T) {
	srv := NewTestServer()

	// Use in-process transport to dispatch directly.
	tc := NewTestClient(t, srv)
	result := tc.ToolCall("echo", map[string]any{"message": "post-handshake"})
	if result != "echo: post-handshake" {
		t.Errorf("tool call after handshake returned %q, want %q", result, "echo: post-handshake")
	}
}

// TestForAllTransports_RunsAllFour verifies that ForAllTransports executes the
// callback exactly 4 times — once for each transport type (Streamable HTTP,
// SSE, in-process memory, stdio). This ensures transport-agnostic tests get
// full coverage.
func TestForAllTransports_RunsAllFour(t *testing.T) {
	var count atomic.Int32
	ForAllTransports(t, NewTestServer(), func(t *testing.T, c *client.Client) {
		count.Add(1)
	})
	if count.Load() != 4 {
		t.Errorf("ForAllTransports ran %d times, want 4", count.Load())
	}
}

// TestForAllTransports_EchoAllTransports verifies the echo tool produces
// identical results across all 4 transport types. This is the canonical test
// for transport-agnostic behavior — if it passes, the tool works regardless
// of how client and server communicate.
func TestForAllTransports_EchoAllTransports(t *testing.T) {
	ForAllTransports(t, NewTestServer(), func(t *testing.T, c *client.Client) {
		text, err := c.ToolCall("echo", map[string]any{"message": "transport-test"})
		if err != nil {
			t.Fatalf("ToolCall: %v", err)
		}
		if text != "echo: transport-test" {
			t.Errorf("echo returned %q, want %q", text, "echo: transport-test")
		}
	})
}

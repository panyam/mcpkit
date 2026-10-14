package e2e_test

// Stdio interop against the TypeScript SDK's reference server
// (@modelcontextprotocol/server-everything). Every mcpkit stdio test drives an
// mcpkit client against an mcpkit server, so a framing both sides got wrong
// the same way passes them all; this is the check that does not share our
// code (#1517).
//
// Opt-in, since it fetches from npm: MCPKIT_STDIO_INTEROP=1 go test -run StdioInterop

import (
	"os"
	"os/exec"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
)

func TestStdioInterop_TypeScriptReferenceServer(t *testing.T) {
	if os.Getenv("MCPKIT_STDIO_INTEROP") != "1" {
		t.Skip("set MCPKIT_STDIO_INTEROP=1 to run (needs npx and network)")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx not on PATH")
	}

	transport := client.NewCommandTransport(npx, []string{"-y", "@modelcontextprotocol/server-everything", "stdio"},
		client.WithShutdownTimeout(2*time.Second),
	)
	c := client.NewClient("", core.ClientInfo{Name: "mcpkit-interop", Version: "1.0"},
		client.WithTransport(transport),
		client.WithConnectTimeout(90*time.Second),
	)
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("connect to server-everything: %v\nstderr:\n%s", err, transport.Stderr())
	}
	defer c.Close()

	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("server-everything listed no tools")
	}

	got, err := c.ToolCall(t.Context(), "echo", map[string]any{"message": "over stdio"})
	if err != nil {
		t.Fatalf("tools/call echo: %v", err)
	}
	if got != "Echo: over stdio" {
		t.Errorf("echo = %q, want %q", got, "Echo: over stdio")
	}
}

package server_test

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunIO_EchoTool verifies that RunIO works over io.Pipe pairs — a server
// and client communicating via in-memory pipes with Content-Length framing.
// This is the same mechanism used by Unix domain sockets, named pipes, etc.
func TestRunIO_EchoTool(t *testing.T) {
	type echoInput struct {
		Message string `json:"message"`
	}
	srv := server.NewServer(core.ServerInfo{Name: "io-test", Version: "1.0"})
	srv.Register(core.TextTool[echoInput]("echo", "Echo input",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			return "echo: " + input.Message, nil
		},
	))

	// Create two pipe pairs: server reads from sr, writes to sw.
	// Client reads from cr (= sw), writes to cw (= sr).
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	// Run server in background. Close sw when done to unblock client reader.
	go func() {
		srv.RunIO(ctx, sr, sw)
		sw.Close()
	}()

	// Connect client over the other end of the pipes.
	c := client.NewClient("io://test", core.ClientInfo{Name: "io-client", Version: "1.0"},
		client.WithIOTransport(cr, cw),
	)
	require.NoError(t, c.Connect())

	// Call the echo tool.
	result, err := c.ToolCall("echo", map[string]any{"message": "hello pipes"})
	require.NoError(t, err)
	assert.Equal(t, "echo: hello pipes", result)

	// Clean shutdown: close client, cancel server context.
	c.Close()
	cancel()
}

// TestRunIO_MultipleToolCalls verifies that multiple sequential tool calls work
// over the same IO transport connection without corruption or state leakage.
func TestRunIO_MultipleToolCalls(t *testing.T) {
	type addInput struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	srv := server.NewServer(core.ServerInfo{Name: "io-test", Version: "1.0"})
	srv.Register(core.TextTool[addInput]("add", "Add two numbers",
		func(ctx core.ToolContext, input addInput) (string, error) {
			return fmt.Sprintf("%d", input.A+input.B), nil
		},
	))

	sr, cw := io.Pipe()
	cr, sw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		srv.RunIO(ctx, sr, sw)
		sw.Close()
	}()

	c := client.NewClient("io://test", core.ClientInfo{Name: "io-client", Version: "1.0"},
		client.WithIOTransport(cr, cw),
	)
	require.NoError(t, c.Connect())
	defer func() { c.Close(); cancel() }()

	// Multiple calls on the same connection.
	for i := 0; i < 5; i++ {
		result, err := c.ToolCall("add", map[string]any{"a": i, "b": 10})
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("%d", i+10), result)
	}
}

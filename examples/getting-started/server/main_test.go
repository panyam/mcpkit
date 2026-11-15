package main

import (
	"context"
	"io"
	"net/http/httptest"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TestScenario runs the Get Started scenario against newServer on each way a
// client can reach it. The stdio pipe carries the same newline-delimited JSON
// as the browser build's postMessage channel, so it covers that encoding path
// too. Under GOOS=js the HTTP row skips, since wasm can't listen.
func TestScenario(t *testing.T) {
	info := core.ClientInfo{Name: "scenario", Version: "1"}
	wires := []struct {
		name    string
		connect func(t *testing.T) *client.Client
	}{
		{"in-process", func(t *testing.T) *client.Client {
			return client.NewClient("memory://", info,
				client.WithTransport(server.NewInProcessTransport(newServer())))
		}},
		{"streamable-http", func(t *testing.T) *client.Client {
			if runtime.GOOS == "js" {
				t.Skip("wasm can't listen")
			}
			ts := httptest.NewServer(newServer().Handler(server.WithStreamableHTTP(true)))
			t.Cleanup(ts.Close)
			return client.NewClient(ts.URL+"/mcp", info)
		}},
		{"stdio-pipe", func(t *testing.T) *client.Client {
			ctx, cancel := context.WithCancel(context.Background())
			sr, cw := io.Pipe()
			cr, sw := io.Pipe()
			// A stdio server's process exits when RunIO returns. Closing sw
			// does the same job here, and lets the client's Close finish.
			go func() { newServer().RunIO(ctx, sr, sw); sw.Close() }()
			t.Cleanup(cancel)
			return client.NewClient("", info, client.WithIOTransport(cr, cw))
		}},
	}
	for _, w := range wires {
		t.Run(w.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := w.connect(t)
			if err := c.Connect(ctx); err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer c.Close()

			tools, err := c.ListTools(ctx)
			if err != nil {
				t.Fatalf("tools/list: %v", err)
			}
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Name)
			}
			if !slices.Equal(names, []string{"greet"}) {
				t.Fatalf("tools/list = %v, want [greet]", names)
			}

			out, err := c.ToolCall(ctx, "greet", map[string]any{"name": "world"})
			if err != nil {
				t.Fatalf("greet: %v", err)
			}
			if out != "Hello, world!" {
				t.Fatalf("greet = %q, want %q", out, "Hello, world!")
			}
		})
	}
}

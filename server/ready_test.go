package server_test

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func readyTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "ready-test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "ping", Description: "ping", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("pong"), nil
		},
	)
	return srv
}

// TestReadyUnblocksWithoutSleep is the regression test for the start-up race.
// The documented quickstart is `go srv.Run(addr)` followed by a client
// connect, which previously forced a time.Sleep because nothing reported when
// the listener was bound. Waiting on Ready must be sufficient on its own.
func TestReadyUnblocksWithoutSleep(t *testing.T) {
	srv := readyTestServer()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run("127.0.0.1:0", server.WithStreamableHTTP(true)) }()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Ready never closed")
	}

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr empty after Ready closed")
	}

	// No sleep anywhere: if Ready is honest, this connects on the first try.
	c := client.NewClient("http://"+addr+"/mcp", core.ClientInfo{Name: "c", Version: "1.0"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect immediately after Ready: %v", err)
	}
	defer c.Close()

	out, err := c.ToolCall(t.Context(), "ping", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if out != "pong" {
		t.Errorf("got %q, want %q", out, "pong")
	}
}

// TestAddrResolvesPortZero verifies ":0" is usable — the kernel-assigned port
// is readable from Addr, so tests and local runs need not hardcode a port.
func TestAddrResolvesPortZero(t *testing.T) {
	srv := readyTestServer()
	if got := srv.Addr(); got != "" {
		t.Errorf("Addr before start = %q, want empty", got)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run("127.0.0.1:0", server.WithStreamableHTTP(true)) }()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Ready never closed")
	}

	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", srv.Addr(), err)
	}
	if port == "0" || port == "" {
		t.Errorf("port = %q, want a kernel-assigned port", port)
	}
}

// TestRunWithListener verifies the caller-owned-bind path, where the race
// cannot exist because the listener is already accepting before serving.
func TestRunWithListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()

	srv := readyTestServer()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.RunWithListener(ln, server.WithStreamableHTTP(true)) }()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Ready never closed")
	}

	if srv.Addr() != addr {
		t.Errorf("Addr = %q, want %q (the caller's listener)", srv.Addr(), addr)
	}

	c := client.NewClient("http://"+addr+"/mcp", core.ClientInfo{Name: "c", Version: "1.0"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
}

// TestReadyIsIdempotent guards the close-once invariant — a second markReady
// must not panic on an already-closed channel.
func TestReadyIsIdempotent(t *testing.T) {
	srv := readyTestServer()

	// Ready is safe to call before start and returns the same channel.
	a, b := srv.Ready(), srv.Ready()
	if a != b {
		t.Error("Ready returned different channels")
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run("127.0.0.1:0", server.WithStreamableHTTP(true)) }()
	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Ready never closed")
	}

	// Already-closed channel stays readable.
	select {
	case <-srv.Ready():
	default:
		t.Error("Ready channel not closed on second read")
	}
}

// TestBindFailureLeavesReadyOpen documents the failure contract: a server that
// cannot bind never becomes ready, so callers must select on Ready and the
// Run error together.
func TestBindFailureLeavesReadyOpen(t *testing.T) {
	// Occupy a port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	srv := readyTestServer()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ln.Addr().String(), server.WithStreamableHTTP(true)) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a bind error on an occupied port")
		}
	case <-srv.Ready():
		t.Fatal("Ready closed even though the bind failed")
	case <-time.After(5 * time.Second):
		t.Fatal("neither Ready nor an error arrived")
	}
}

// TestRegisterPanicsOnUnsupportedType covers the silent-drop fix: Register
// takes ...any, so a wrong type cannot be caught by the compiler and used to
// vanish with no diagnostic at all.
func TestRegisterPanicsOnUnsupportedType(t *testing.T) {
	cases := []struct {
		name string
		item any
	}{
		{"pointer to Tool", &server.Tool{ToolDef: core.ToolDef{Name: "p"}}},
		{"string", "not a tool"},
		{"int", 42},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := server.NewServer(core.ServerInfo{Name: "s", Version: "1.0"})
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("Register did not panic; the value was silently dropped")
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "server.Register: unsupported type") {
					t.Errorf("panic message = %v, want the Register diagnostic", r)
				}
			}()
			srv.Register(tc.item)
		})
	}
}

// TestRegisterAcceptsSupportedTypes guards against the panic being too eager.
func TestRegisterAcceptsSupportedTypes(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "s", Version: "1.0"})
	srv.Register(
		server.Tool{
			ToolDef: core.ToolDef{Name: "a", InputSchema: map[string]any{"type": "object"}},
			Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
				return core.TextResult("a"), nil
			},
		},
		server.Resource{
			ResourceDef: core.ResourceDef{URI: "test://b"},
			Handler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
				return core.ResourceResult{}, nil
			},
		},
	)
	if _, ok := srv.Registry().ToolDef("a"); !ok {
		t.Error("tool 'a' was not registered")
	}
}

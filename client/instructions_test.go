package client_test

import (
	"net/http/httptest"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

const instructionsText = "Call search before fetch."

// newInstructionsClient connects a client in the given mode to a real server
// configured with instructions, so the test covers both halves of the wire.
func newInstructionsClient(t *testing.T, srv *server.Server, info core.Implementation, mode client.ClientMode) *client.Client {
	t.Helper()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", info, client.WithClientMode(mode))
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestClientReadsInstructions covers issue 1560 from the client side: the
// legacy wire reads InitializeResult.instructions and the stateless wire reads
// DiscoverResult.instructions.
func TestClientReadsInstructions(t *testing.T) {
	for _, mode := range []client.ClientMode{client.ClientModeLegacyOnly, client.ClientModeStateless} {
		t.Run(mode.String(), func(t *testing.T) {
			srv := server.NewServer(core.Implementation{Name: "demo", Version: "1.0"}, server.WithInstructions(instructionsText))
			c := newInstructionsClient(t, srv, core.Implementation{Name: "c", Version: "1"}, mode)
			if c.Instructions != instructionsText {
				t.Errorf("Instructions = %q, want %q", c.Instructions, instructionsText)
			}
			if c.ServerInfo.Name != "demo" {
				t.Errorf("ServerInfo.Name = %q, want demo", c.ServerInfo.Name)
			}
		})
	}
}

// TestClientSendsFullClientInfoOnStatelessWire pins the per-request
// _meta clientInfo to the whole Implementation. It was built from name and
// version alone, so title and the 2025-11-25 fields never reached a server.
func TestClientSendsFullClientInfoOnStatelessWire(t *testing.T) {
	var got *core.Implementation
	srv := server.NewServer(core.Implementation{Name: "demo", Version: "1.0"})
	srv.RegisterTool(core.ToolDef{Name: "whoami"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		if meta := core.RequestMetaFromContext(ctx); meta != nil {
			got = meta.ClientInfo
		}
		return core.TextResult("ok"), nil
	})
	info := core.Implementation{
		Name:        "c",
		Title:       "Client C",
		Version:     "1",
		Description: "A test client.",
		WebsiteURL:  "https://example.com/c",
		Icons:       []core.Icon{{Src: "https://example.com/c.png"}},
	}
	c := newInstructionsClient(t, srv, info, client.ClientModeStateless)

	if _, err := c.ToolCall(t.Context(), "whoami", nil); err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if got == nil {
		t.Fatal("server saw no _meta clientInfo")
	}
	if got.Title != info.Title || got.Description != info.Description || got.WebsiteURL != info.WebsiteURL {
		t.Errorf("clientInfo = %+v, want title/description/websiteUrl from %+v", *got, info)
	}
	if len(got.Icons) != 1 || got.Icons[0].Src != info.Icons[0].Src {
		t.Errorf("clientInfo.icons = %+v, want %+v", got.Icons, info.Icons)
	}
}

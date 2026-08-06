package client_test

import (
	"context"
	"fmt"
	"log"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
)

// Connecting over the legacy SSE transport (MCP 2024-11-05). The default is
// Streamable HTTP; pass WithSSEClient only when talking to a server that has
// not moved off the two-endpoint SSE wire.
//
// This example is illustrative and does not run: it needs a live server.
func ExampleWithSSEClient() {
	ctx := context.Background()

	c := client.NewClient("http://localhost:8080/sse",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithSSEClient(),
	)
	// Connect's context bounds the handshake, not the session that follows.
	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(tools))
}

// Paging through a list method. mcpkit's own server returns everything in one
// page, but a client cannot assume that of an arbitrary server: loop until
// NextCursor comes back empty.
//
// Use ListToolsPage rather than ListTools when you want the envelope, since it
// also carries the SEP-2549 ttlMs and cacheScope caching hints that the
// item-iterator forms discard.
//
// This example is illustrative and does not run: it needs a live server.
func ExampleClient_ListToolsPage() {
	ctx := context.Background()

	c := client.NewClient("http://localhost:8080/mcp",
		core.ClientInfo{Name: "example", Version: "1.0"})
	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	var all []core.ToolDef
	cursor := ""
	for {
		// One context for the whole walk, so cancelling it stops the paging.
		page, err := c.ListToolsPage(ctx, cursor)
		if err != nil {
			log.Fatal(err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	fmt.Println(len(all))
}

// Telling the server the client's filesystem roots changed. The server responds
// by re-issuing roots/list, so the notification carries no payload of its own.
// Registering a RootsHandler is what advertises the capability; without one the
// server never asks.
//
// Deprecated: roots is deprecated per SEP-2577. See docs/SEP_2577_DEPRECATIONS.md.
//
// This example is illustrative and does not run: it needs a live server.
func ExampleClient_NotifyRootsChanged() {
	ctx := context.Background()

	roots := []core.Root{{URI: "file:///home/me/project", Name: "project"}}

	c := client.NewClient("http://localhost:8080/mcp",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithRootsHandler(func(context.Context) ([]core.Root, error) {
			return roots, nil
		}),
	)
	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	roots = append(roots, core.Root{URI: "file:///tmp/scratch", Name: "scratch"})
	if err := c.NotifyRootsChanged(ctx); err != nil {
		log.Fatal(err)
	}
}

package main

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/disclosure/mcp"
	"github.com/panyam/mcpkit/experimental/ext/groups"
	"github.com/panyam/mcpkit/server"
)

// TestNestedGroupUnderTwoParents checks the fixture's nesting, which the
// playground shows and the groups conformance scenario grades: one child
// group, under two parents, absent from groups/list, resolvable by name.
func TestNestedGroupUnderTwoParents(t *testing.T) {
	ctx := context.Background()
	srv := server.NewServer(core.ServerInfo{Name: "disclosure", Version: "0.1.0"})
	if err := mcp.Register(srv); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, groups.ClientOption())
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gc := groups.NewClient(c)

	top, err := gc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != mcp.ThemeCount {
		t.Fatalf("groups/list has %d groups, want the %d themes", len(top), mcp.ThemeCount)
	}
	for _, g := range top {
		if g.Name == mcp.NestedGroup {
			t.Fatalf("%s is nested, so groups/list must leave it out", g.Name)
		}
	}
	for _, parent := range mcp.NestedGroupParents {
		got, err := gc.Get(ctx, parent)
		if err != nil {
			t.Fatalf("groups/get %s: %v", parent, err)
		}
		if !slices.ContainsFunc(got.Groups, func(g groups.Group) bool { return g.Name == mcp.NestedGroup }) {
			t.Errorf("groups/get %s does not list %s", parent, mcp.NestedGroup)
		}
	}
	child, err := gc.Get(ctx, mcp.NestedGroup)
	if err != nil {
		t.Fatalf("groups/get %s: %v", mcp.NestedGroup, err)
	}
	if child.Instructions == "" {
		t.Errorf("groups/get %s returned no instructions", mcp.NestedGroup)
	}
}

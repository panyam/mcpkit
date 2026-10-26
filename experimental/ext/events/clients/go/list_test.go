package eventsclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
)

// pagedListClient connects to a server whose events/list serves names in
// pages of pageSize, or, with endless, always sends another cursor.
func pagedListClient(t *testing.T, names []string, pageSize int, endless bool) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "paged-events", Version: "1"})
	srv.HandleMethod("events/list", func(_ core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(params, &p)
		start := 0
		fmt.Sscanf(p.Cursor, "%d", &start)
		end := min(start+pageSize, len(names))
		page := struct {
			Events     []events.EventDef `json:"events"`
			NextCursor string            `json:"nextCursor,omitempty"`
		}{}
		for _, n := range names[start:end] {
			page.Events = append(page.Events, events.EventDef{Name: n, Delivery: []string{"poll"}})
		}
		if end < len(names) || endless {
			page.NextCursor = fmt.Sprint(end)
		}
		return core.NewResponse(id, page)
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"})
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestList_FollowsEveryPage(t *testing.T) {
	names := []string{"a.one", "a.two", "b.one", "b.two", "c.one"}
	defs, err := eventsclient.List(context.Background(), pagedListClient(t, names, 2, false))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, d := range defs {
		got = append(got, d.Name)
	}
	if fmt.Sprint(got) != fmt.Sprint(names) {
		t.Errorf("List = %v, want every page in order %v", got, names)
	}
}

func TestList_RealServerAnswersInOnePage(t *testing.T) {
	c, _, _ := stack(t)
	defs, err := eventsclient.List(context.Background(), c)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	// events.Register also advertises its own events.topology source.
	if !slices.Contains(names, "fake.event") {
		t.Errorf("List = %v, want it to include the registered source fake.event", names)
	}
}

func TestList_EndlessCursorIsAnError(t *testing.T) {
	_, err := eventsclient.List(context.Background(), pagedListClient(t, []string{"a.one"}, 1, true))
	if !errors.Is(err, eventsclient.ErrListOverrun) {
		t.Fatalf("List with a cursor that never ends = %v, want ErrListOverrun", err)
	}
}

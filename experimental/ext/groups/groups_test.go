package groups

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"billing":               true,
		"deploy-prod":           true,
		"a1":                    true,
		strings.Repeat("a", 64): true,
		"":                      false,
		strings.Repeat("a", 65): false,
		"Billing":               false,
		"-billing":              false,
		"billing-":              false,
		"deploy--prod":          false,
		"deploy_prod":           false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAddRejects(t *testing.T) {
	c := NewCatalog()
	if err := c.Add(Def{Name: "Bad", Tools: []string{"t"}}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad name: %v", err)
	}
	if err := c.Add(Def{Name: "empty"}); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty group: %v", err)
	}
	if err := c.Add(Def{Name: "ok", Instructions: "only prose"}); err != nil {
		t.Errorf("instructions-only group: %v", err)
	}
	if err := c.Add(Def{Name: "ok", Tools: []string{"t"}}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate: %v", err)
	}
}

func TestRegisterWithUnknownMember(t *testing.T) {
	for _, d := range []Def{
		{Name: "g", Tools: []string{"missing"}},
		{Name: "g", Prompts: []string{"missing"}},
		{Name: "g", Resources: []string{"test://missing"}},
		{Name: "g", Groups: []string{"missing"}},
	} {
		srv := server.NewServer(core.ServerInfo{Name: "t", Version: "0"})
		c := NewCatalog()
		if err := c.Add(d); err != nil {
			t.Fatal(err)
		}
		if err := c.RegisterWith(srv); !errors.Is(err, ErrUnknownMember) {
			t.Errorf("%+v: err = %v, want ErrUnknownMember", d, err)
		}
	}
}

// fixture serves four tools, one prompt and one resource. Groups:
//
//	a: tools t1, t2
//	b: tool t3, resource test://r1, child c
//	c: prompt p1, child d
//	d: child c (a cycle with c)
//
// t4 is in no group. So groups/list holds a and b, and c and d are reachable
// only through b.
func fixture(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "groups-test", Version: "0"})
	for _, name := range []string{"t1", "t2", "t3", "t4"} {
		schema := map[string]any{"type": "object"}
		if name == "t1" {
			schema["properties"] = map[string]any{"doc": core.FileInputProperty(core.FileInputDescriptor{})}
		}
		srv.RegisterTool(core.ToolDef{Name: name, InputSchema: schema},
			func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
	}
	srv.RegisterPrompt(core.PromptDef{Name: "p1"},
		func(core.PromptContext, core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		})
	srv.RegisterResource(core.ResourceDef{URI: "test://r1", Name: "r1"},
		func(core.ResourceContext, core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		})

	c := NewCatalog()
	for _, d := range []Def{
		{Name: "a", Description: "A tools", Tools: []string{"t1", "t2"}},
		{Name: "b", Description: "B tools", Instructions: "Use t3.", Tools: []string{"t3"}, Resources: []string{"test://r1"}, Groups: []string{"c"}},
		{Name: "c", Description: "C prompts", Prompts: []string{"p1"}, Groups: []string{"d"}},
		{Name: "d", Description: "D loops back", Groups: []string{"c"}},
	} {
		if err := c.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RegisterWith(srv); err != nil {
		t.Fatal(err)
	}
	return srv
}

type wire struct {
	name   string
	server stateless.Mode
	client client.ClientMode
}

var wires = []wire{
	{"legacy", stateless.ModeLegacyOnly, client.ClientModeLegacyOnly},
	{"stateless", stateless.ModeStateless, client.ClientModeStateless},
}

func connect(t *testing.T, srv *server.Server, w wire, opts ...client.ClientOption) *client.Client {
	t.Helper()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(w.server)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "groups-test", Version: "0"},
		append([]client.ClientOption{client.WithClientMode(w.client)}, opts...)...)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("%s connect: %v", w.name, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func listNames[T any](t *testing.T, c *client.Client, method, key string, name func(T) string) []string {
	t.Helper()
	res, err := c.Call(context.Background(), method, map[string]any{})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var raw map[string]json.RawMessage
	if err := res.Unmarshal(&raw); err != nil {
		t.Fatal(err)
	}
	var items []T
	if err := json.Unmarshal(raw[key], &items); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range items {
		names = append(names, name(it))
	}
	return names
}

func toolNames(t *testing.T, c *client.Client) []string {
	return listNames(t, c, "tools/list", "tools", func(d core.ToolDef) string { return d.Name })
}

// TestWireParity runs each groups rule on the session wire and the stateless
// wire. Row names lead with the ticket, as in server.TestWireParity, so
// make exercise-wire can print them.
func TestWireParity(t *testing.T) {
	ctx := context.Background()

	t.Run("1685 groups/list returns top-level groups only", func(t *testing.T) {
		for _, w := range wires {
			gs, err := NewClient(connect(t, fixture(t), w, ClientOption())).List(ctx)
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			var names []string
			for _, g := range gs {
				names = append(names, g.Name)
			}
			if !slices.Equal(names, []string{"a", "b"}) {
				t.Errorf("%s: groups/list = %v, want [a b]", w.name, names)
			}
		}
	})

	t.Run("1685 groups/get returns full definitions and child summaries", func(t *testing.T) {
		for _, w := range wires {
			g, err := NewClient(connect(t, fixture(t), w, ClientOption())).Get(ctx, "b")
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			if g.Instructions != "Use t3." || len(g.Tools) != 1 || g.Tools[0].Name != "t3" || g.Tools[0].InputSchema == nil {
				t.Errorf("%s: tools/instructions = %q %+v", w.name, g.Instructions, g.Tools)
			}
			if len(g.Resources) != 1 || g.Resources[0].URI != "test://r1" {
				t.Errorf("%s: resources = %+v", w.name, g.Resources)
			}
			if len(g.Groups) != 1 || g.Groups[0].Name != "c" || g.Groups[0].Description != "C prompts" {
				t.Errorf("%s: child groups = %+v", w.name, g.Groups)
			}
			if g.CacheScope != core.CacheScopePrivate || g.TTLMs == nil {
				t.Errorf("%s: cache hints ttlMs=%v cacheScope=%q", w.name, g.TTLMs, g.CacheScope)
			}
		}
	})

	t.Run("1685 groups/get on an unknown name is -32002", func(t *testing.T) {
		for _, w := range wires {
			_, err := NewClient(connect(t, fixture(t), w, ClientOption())).Get(ctx, "nope")
			var rpc *client.RPCError
			if !errors.As(err, &rpc) || rpc.Code != ErrCodeGroupNotFound {
				t.Errorf("%s: err = %v, want code %d", w.name, err, ErrCodeGroupNotFound)
			}
		}
	})

	t.Run("1685 groups client loses grouped primitives from the base lists", func(t *testing.T) {
		for _, w := range wires {
			c := connect(t, fixture(t), w, ClientOption())
			if got := toolNames(t, c); !slices.Equal(got, []string{"t4"}) {
				t.Errorf("%s: tools/list = %v, want [t4]", w.name, got)
			}
			if got := listNames(t, c, "prompts/list", "prompts", func(d core.PromptDef) string { return d.Name }); len(got) != 0 {
				t.Errorf("%s: prompts/list = %v, want none", w.name, got)
			}
			if got := listNames(t, c, "resources/list", "resources", func(d core.ResourceDef) string { return d.URI }); len(got) != 0 {
				t.Errorf("%s: resources/list = %v, want none", w.name, got)
			}
		}
	})

	t.Run("1685 plain client gets the flat lists", func(t *testing.T) {
		for _, w := range wires {
			c := connect(t, fixture(t), w)
			if got := toolNames(t, c); !slices.Equal(got, []string{"t1", "t2", "t3", "t4"}) {
				t.Errorf("%s: tools/list = %v, want all four", w.name, got)
			}
		}
	})

	t.Run("1685 a grouped tool is callable without fetching its group", func(t *testing.T) {
		for _, w := range wires {
			c := connect(t, fixture(t), w, ClientOption())
			if _, err := c.Call(ctx, "tools/call", map[string]any{"name": "t1", "arguments": map[string]any{}}); err != nil {
				t.Errorf("%s: tools/call t1: %v", w.name, err)
			}
		}
	})

	t.Run("1685 groups/get strips x-mcp-file for clients without fileInputs", func(t *testing.T) {
		for _, w := range wires {
			g, err := NewClient(connect(t, fixture(t), w, ClientOption())).Get(ctx, "a")
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			for _, tool := range g.Tools {
				if tool.Name == "t1" && strings.Contains(mustJSON(t, tool.InputSchema), core.FileInputSchemaKey) {
					t.Errorf("%s: t1 schema still carries %s", w.name, core.FileInputSchemaKey)
				}
			}
		}
	})

	t.Run("1685 Walk visits each nested group once despite the cycle", func(t *testing.T) {
		for _, w := range wires {
			var seen []string
			err := NewClient(connect(t, fixture(t), w, ClientOption())).Walk(ctx, "b", func(name string, _ GetGroupResult) error {
				seen = append(seen, name)
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			if !slices.Equal(seen, []string{"b", "c", "d"}) {
				t.Errorf("%s: walk = %v, want [b c d]", w.name, seen)
			}
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

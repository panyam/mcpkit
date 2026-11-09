package rooms_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
)

// The page side of WIRE.md, replayed from testdata/wire/page.jsonl. The
// TypeScript page replays the same file (ts/test/wire.test.ts), so the
// document and both pages cannot drift apart. The test plays the bridge as a
// bare relay peer.

type wireStep struct {
	Note        string          `json:"note"`
	Page        string          `json:"page"`
	Bridge      json.RawMessage `json:"bridge"`
	PageSends   json.RawMessage `json:"page_sends"`
	SendNothing bool            `json:"page_sends_nothing"`

	Tool    *fixtureTool    `json:"tool"`
	Decl    *fixtureDecl    `json:"decl"`
	Label   string          `json:"label"`
	Allowed bool            `json:"allowed"`
	Name    string          `json:"name"`
	Data    json.RawMessage `json:"data"`
	Code    string          `json:"code"`
}

type fixtureTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	ReadOnly    bool           `json:"readOnly"`
}

type fixtureDecl struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	PayloadSchema map[string]any `json:"payloadSchema"`
	ContextHint   map[string]any `json:"contextHint"`
}

func readWireFixture(t *testing.T, path string) []wireStep {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var steps []wireStep
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		var s wireStep
		require.NoError(t, json.Unmarshal(sc.Bytes(), &s), "%s:%d", path, line)
		steps = append(steps, s)
	}
	require.NoError(t, sc.Err())
	return steps
}

// wireSubset reports whether got carries everything want does. Objects may
// have more fields than want names, arrays match element by element, and the
// string "$ANY" matches any value that is present.
func wireSubset(want, got any) bool {
	if s, ok := want.(string); ok && s == "$ANY" {
		return true
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present || !wireSubset(wv, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !wireSubset(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		return string(wb) == string(gb)
	}
}

func fixtureToolOf(ft *fixtureTool) rooms.PageTool {
	return rooms.PageTool{
		Name: ft.Name, Description: ft.Description, InputSchema: ft.InputSchema, ReadOnly: ft.ReadOnly,
		Execute: func(_ context.Context, call rooms.PageCall) (core.ToolResult, error) {
			var args struct {
				Sheet string `json:"sheet"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			return core.TextResult("showing " + args.Sheet), nil
		},
	}
}

// nextFrame is the next mcp/1 message the bridge peer receives, or nil after
// wait.
func nextFrame(r rooms.Room, wait time.Duration) json.RawMessage {
	deadline := time.After(wait)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return nil
			}
			if ev.Type == rooms.MessageReceived && ev.Kind == rooms.MessageKind {
				return ev.Payload
			}
		case <-deadline:
			return nil
		}
	}
}

func TestWireFixture_GoPageReplaysPageJSONL(t *testing.T) {
	steps := readWireFixture(t, "testdata/wire/page.jsonl")
	relay := memroom.New()
	ctx := t.Context()
	bridge, err := relay.Transport("bridge").Join(ctx, "fixture:wire")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bridge.Leave() })
	page, err := rooms.JoinPage(ctx, relay.Transport("alice"), "fixture:wire")
	require.NoError(t, err)
	t.Cleanup(page.Leave)

	for i, s := range steps {
		where := fmt.Sprintf("step %d", i+1)
		switch {
		case s.Note != "":
		case s.Page != "":
			switch s.Page {
			case "register_tool":
				page.RegisterTool(fixtureToolOf(s.Tool))
			case "declare_event":
				page.DeclareEvent(rooms.PageEventDecl{Name: s.Decl.Name, Description: s.Decl.Description,
					PayloadSchema: s.Decl.PayloadSchema, ContextHint: s.Decl.ContextHint})
			case "set_label":
				page.SetLabel(s.Label)
			case "set_agents_allowed":
				page.SetAgentsAllowed(s.Allowed)
			case "emit":
				var data any
				require.NoError(t, json.Unmarshal(s.Data, &data))
				require.NoError(t, page.Emit(s.Name, data))
			case "accept_pairing":
				page.AcceptPairing(s.Code)
			default:
				t.Fatalf("%s: unknown page action %q", where, s.Page)
			}
		case s.Bridge != nil:
			require.NoError(t, bridge.Send(ctx, page.ID(), rooms.MessageKind, s.Bridge), where)
		case s.PageSends != nil:
			got := nextFrame(bridge, 2*time.Second)
			require.NotNil(t, got, "%s: the page sent nothing, want %s", where, s.PageSends)
			var w, g any
			require.NoError(t, json.Unmarshal(s.PageSends, &w))
			require.NoError(t, json.Unmarshal(got, &g))
			require.True(t, wireSubset(w, g), "%s:\n want %s\n  got %s", where, s.PageSends, got)
		case s.SendNothing:
			got := nextFrame(bridge, 300*time.Millisecond)
			require.Nil(t, got, "%s: the page sent %s", where, got)
		default:
			t.Fatalf("%s: empty step", where)
		}
	}
}

func TestWireSubset_MatchesWhatTheFixtureNeeds(t *testing.T) {
	parse := func(s string) any {
		var v any
		require.NoError(t, json.Unmarshal([]byte(s), &v))
		return v
	}
	require.True(t, wireSubset(parse(`{"a":1}`), parse(`{"a":1,"b":2}`)), "extra fields are fine")
	require.False(t, wireSubset(parse(`{"a":1}`), parse(`{"a":2}`)))
	require.False(t, wireSubset(parse(`{"a":1}`), parse(`{}`)), "a named field must be there")
	require.True(t, wireSubset(parse(`{"a":"$ANY"}`), parse(`{"a":[1]}`)))
	require.False(t, wireSubset(parse(`[1,2]`), parse(`[1,2,3]`)), "arrays match in length")
	require.False(t, wireSubset(parse(`{"a":1}`), parse(`{"a":"1"}`)), "types matter")
	require.False(t, wireSubset(parse(`{"a":"$ANY"}`), parse(`{}`)), "$ANY still needs the field")
}

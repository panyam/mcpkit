package roomsevents_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	rt "github.com/panyam/mcpkit/experimental/integrations/roomsevents/internal/roomstest"
)

var itemFound = rooms.PageEventDecl{
	Name:        "item.found",
	Description: "The player found an item.",
	PayloadSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"item": map[string]any{"type": "string"}},
		"required":   []any{"item"},
	},
	ContextHint: map[string]any{"priority": "high", "template": "found {{item}}"},
}

type listedType struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	PayloadSchema map[string]any `json:"payloadSchema"`
	Meta          map[string]any `json:"_meta"`
}

func eventsList(t *testing.T, c *client.Client) (map[string]listedType, string) {
	t.Helper()
	raw, err := c.Call(t.Context(), "events/list", map[string]any{})
	require.NoError(t, err)
	var res struct {
		Events []listedType `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &res))
	out := map[string]listedType{}
	for _, e := range res.Events {
		out[e.Name] = e
	}
	return out, string(raw.Raw)
}

func TestDeclared_FixedCatalogIsListedWithSchemaAndHintAndNeverChanges(t *testing.T) {
	st := rt.New(t, rooms.WithPageEventCatalog([]rooms.PageEventDecl{itemFound}))
	a := st.Agent(t, "alice")

	types, first := eventsList(t, a)
	got, ok := types["item.found"]
	require.True(t, ok, "the declared type is listed before any page joins")
	assert.Equal(t, itemFound.Description, got.Description)
	assert.Equal(t, itemFound.ContextHint, got.Meta[rooms.MetaContextHint])
	props := got.PayloadSchema["properties"].(map[string]any)
	assert.Contains(t, props, "item", "the page's own fields")
	assert.Contains(t, props, "user", "and the origin every event carries")
	assert.Equal(t, []any{"item"}, got.PayloadSchema["required"])

	rt.Attach(t, a, rt.Room)
	p := st.Page(t, "alice", rt.Room)
	p.DeclareEvent(itemFound)
	rt.WaitListed(t, a, p.ID())
	time.Sleep(100 * time.Millisecond)
	_, second := eventsList(t, a)
	assert.JSONEq(t, first, second, "a fixed catalog gives the same events/list on every call")
}

func TestDeclared_PageDeclarationBecomesAnEventTypeAndCarriesItsFields(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)

	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool {
		types, _ := eventsList(t, a)
		_, ok := types["item.found"]
		return ok
	}, 3*time.Second, 20*time.Millisecond, "events/list gains the declared type")

	ch, err := stream(t, a, "item.found", rt.Room)
	require.NoError(t, err)
	require.NoError(t, p.Emit("item.found", map[string]any{"item": "brass key"}))
	ev := next(t, ch)
	var got map[string]any
	require.NoError(t, json.Unmarshal(ev.Data, &got))
	assert.Equal(t, "brass key", got["item"], "the page's field sits at the top level, where a template reaches it")
	assert.Equal(t, "alice", got["user"])
	assert.Equal(t, rt.Room, got["resource"])
	assert.Equal(t, string(p.ID()), got["page"])
}

func TestDeclared_TypeLeavesWithTheLastPageDeclaringIt(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)
	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool {
		types, _ := eventsList(t, a)
		_, ok := types["item.found"]
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	p.Leave()
	require.Eventually(t, func() bool {
		types, _ := eventsList(t, a)
		_, ok := types["item.found"]
		return !ok
	}, 3*time.Second, 20*time.Millisecond)
}

package rooms_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

var itemFound = rooms.PageEventDecl{
	Name:        "item.found",
	Description: "The player found an item.",
	PayloadSchema: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"item": map[string]any{"type": "string"}, "place": map[string]any{"type": "string"}},
		"required":             []any{"item", "place"},
		"additionalProperties": false,
	},
	ContextHint: map[string]any{"priority": "high"},
}

// refusals records what the bridge refused, by event name.
type refusals struct {
	mu  sync.Mutex
	got map[string]string
}

func watchRefusals(p *rooms.Page) *refusals {
	r := &refusals{got: map[string]string{}}
	p.OnEventRefused(func(name, reason string) {
		r.mu.Lock()
		r.got[name] = reason
		r.mu.Unlock()
	})
	return r
}

func (r *refusals) reason(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[name]
}

func (r *refusals) await(t *testing.T, name string) string {
	t.Helper()
	require.Eventually(t, func() bool { return r.reason(name) != "" }, 3*time.Second, 10*time.Millisecond, "no refusal for %s", name)
	return r.reason(name)
}

func catalogNames(b *rooms.Bridge) []string {
	var out []string
	for _, d := range b.EventCatalog() {
		out = append(out, d.Name)
	}
	return out
}

// listedPage is a page the bridge has listed, so its events reach the hook.
func listedPage(t *testing.T, st *stack, a *agent, subject string) *rooms.Page {
	t.Helper()
	p := st.page(t, subject, room)
	a.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })
	return p
}

func TestEvents_PageDeclarationReachesTheCatalog(t *testing.T) {
	_, opt := eventRecorder()
	st := newStack(t, opt)
	var changes atomic.Int32
	st.bridge.OnEventCatalogChange(func() { changes.Add(1) })
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")

	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 1 }, 3*time.Second, 10*time.Millisecond)
	got := st.bridge.EventCatalog()[0]
	assert.Equal(t, itemFound.Name, got.Name)
	assert.Equal(t, itemFound.Description, got.Description)
	assert.Equal(t, itemFound.ContextHint, got.ContextHint, "the hint survives the wire under its _meta key")
	assert.Equal(t, "object", got.PayloadSchema["type"])
	assert.Equal(t, int32(1), changes.Load())
}

func TestEvents_DeclaredEventIsValidated(t *testing.T) {
	ch, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")
	r := watchRefusals(p)
	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 1 }, 3*time.Second, 10*time.Millisecond)

	require.NoError(t, p.Emit("item.found", map[string]any{"item": "brass key", "place": "well"}))
	e := nextEvent(t, ch)
	assert.Equal(t, "item.found", e.Name)
	assert.JSONEq(t, `{"item":"brass key","place":"well"}`, string(e.Data))

	require.NoError(t, p.Emit("item.found", map[string]any{"item": 7}))
	assert.Contains(t, r.await(t, "item.found"), "does not match the declared payload schema")
	noEvent(t, ch)
}

func TestEvents_UndeclaredNamePassesWithoutACatalog(t *testing.T) {
	ch, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")

	require.NoError(t, p.Emit("map.moved", map[string]any{"x": 1}))
	assert.Equal(t, "map.moved", nextEvent(t, ch).Name)
}

func TestEvents_FixedCatalogRefusesUndeclaredNamesAndOtherSchemas(t *testing.T) {
	ch, opt := eventRecorder()
	st := newStack(t, opt, rooms.WithPageEventCatalog([]rooms.PageEventDecl{itemFound}))
	assert.Equal(t, []string{"item.found"}, catalogNames(st.bridge), "the catalog is there before any page")
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")
	r := watchRefusals(p)

	require.NoError(t, p.Emit("map.moved", map[string]any{"x": 1}))
	assert.Contains(t, r.await(t, "map.moved"), "does not declare this event")
	noEvent(t, ch)

	require.NoError(t, p.Ask("where is the key?"))
	assert.Equal(t, rooms.EventAsk, nextEvent(t, ch).Name, "built-in events pass a fixed catalog")

	other := itemFound
	other.PayloadSchema = map[string]any{"type": "object"}
	p.DeclareEvent(other)
	assert.Contains(t, r.await(t, "item.found"), "differs from the bridge's declaration")
	assert.Equal(t, []string{"item.found"}, catalogNames(st.bridge))
	assert.Equal(t, itemFound.PayloadSchema, st.bridge.EventCatalog()[0].PayloadSchema, "a page cannot change a fixed type")
}

func TestEvents_FirstPageFixesADynamicSchema(t *testing.T) {
	_, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p1 := listedPage(t, st, a, "alice")
	p1.DeclareEvent(itemFound)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 1 }, 3*time.Second, 10*time.Millisecond)

	p2 := listedPage(t, st, a, "alice")
	r := watchRefusals(p2)
	other := itemFound
	other.PayloadSchema = map[string]any{"type": "object"}
	p2.DeclareEvent(other)
	assert.Contains(t, r.await(t, "item.found"), "another page already declares this event")
}

func TestEvents_DynamicTypeGoesWithTheLastPageDeclaringIt(t *testing.T) {
	_, opt := eventRecorder()
	st := newStack(t, opt)
	var changes atomic.Int32
	st.bridge.OnEventCatalogChange(func() { changes.Add(1) })
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")
	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 1 }, 3*time.Second, 10*time.Millisecond)

	p.Leave()
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 0 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(2), changes.Load())
}

func TestEvents_RemoveEventWithdrawsTheType(t *testing.T) {
	_, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := listedPage(t, st, a, "alice")
	p.DeclareEvent(itemFound)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 1 }, 3*time.Second, 10*time.Millisecond)

	p.RemoveEvent(itemFound.Name)
	require.Eventually(t, func() bool { return len(st.bridge.EventCatalog()) == 0 }, 3*time.Second, 10*time.Millisecond)
}

func TestEvents_CatalogPanicsOnADeclarationNoPageCouldMatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl rooms.PageEventDecl
		want string
	}{
		{"bad name", rooms.PageEventDecl{Name: "Item Found"}, "not a valid event name"},
		{"built-in", rooms.PageEventDecl{Name: rooms.EventAsk}, "bridge's own event types"},
		{"not an object", rooms.PageEventDecl{Name: "a.b", PayloadSchema: map[string]any{"type": "string"}}, `type "object"`},
		{"origin field", rooms.PageEventDecl{Name: "a.b", PayloadSchema: map[string]any{
			"type": "object", "properties": map[string]any{"user": map[string]any{"type": "string"}},
		}}, `"user"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				require.NotNil(t, r)
				assert.Contains(t, r, tc.want)
			}()
			rooms.New(nil, rooms.WithPageEventCatalog([]rooms.PageEventDecl{tc.decl}))
		})
	}
}

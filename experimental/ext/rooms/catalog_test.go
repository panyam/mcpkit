package rooms_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The #1611 options: a page-tool catalog that changes at runtime on the
// stateless wire, and tools/list filtered by the caller's scopes.

func listPage(t *testing.T, a *agent) *core.ToolsListResult {
	t.Helper()
	res, err := a.ListToolsPage(t.Context(), "")
	require.NoError(t, err)
	return res
}

func namesOf(res *core.ToolsListResult) []string {
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestDynamicCatalog_StatelessSeesToolsOfPagesThatAgreed(t *testing.T) {
	st := newStack(t, rooms.WithDynamicCatalogTTL(2*time.Second))
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	alice := st.agentOn(t, "stateless", "alice", allScopes...)
	bob := st.agentOn(t, "stateless", "bob", allScopes...)
	alice.waitAllowedIn(t, room, p.ID())

	res := listPage(t, alice)
	assert.Contains(t, namesOf(res), "show_sheet")
	require.NotNil(t, res.TTLMs)
	assert.Equal(t, 2000, *res.TTLMs)
	assert.Equal(t, core.CacheScopePrivate, res.CacheScope, "the list differs per caller")
	assert.NotContains(t, namesOf(listPage(t, bob)), "show_sheet", "bob's agent was never agreed to")

	ok, out := call(t, alice, "show_sheet", map[string]any{"room": room, "sheet": "x"})
	assert.True(t, ok, out)
}

func TestDynamicCatalog_ANewToolShowsOnTheNextList(t *testing.T) {
	st := newStack(t, rooms.WithDynamicCatalogTTL(time.Second))
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	a.waitAllowedIn(t, room, p.ID())
	require.NotContains(t, namesOf(listPage(t, a)), "zoom_to")

	p.RegisterTool((&recorder{}).tool("zoom_to", "p", true))
	require.Eventually(t, func() bool { return slices.Contains(namesOf(listPage(t, a)), "zoom_to") }, 3*time.Second, 20*time.Millisecond)
}

func TestDynamicCatalog_OffByDefaultHidesPageToolsWithNoSession(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	a.waitAllowedIn(t, room, p.ID())

	res := listPage(t, a)
	assert.NotContains(t, namesOf(res), "show_sheet")
	assert.NotEqual(t, core.CacheScopePrivate, res.CacheScope)
}

// waitAllowedIn waits until list_pages for resource shows page agreed to a.
func (a *agent) waitAllowedIn(t *testing.T, resource string, page rooms.PeerID) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, p := range a.pages(t, resource) {
			if p.Page == page && p.Allowed {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "page %s never agreed", page)
}

type stepUp struct {
	Tool   string   `json:"tool"`
	Scopes []string `json:"scopes"`
}

func stepUpOf(t *testing.T, a *agent) ([]stepUp, bool) {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), "list_pages", map[string]any{"resource": room})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	var out struct {
		StepUp *[]stepUp `json:"stepUp"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	require.NoError(t, json.Unmarshal(b, &out))
	if out.StepUp == nil {
		return nil, false
	}
	return *out.StepUp, true
}

func TestScopeFilteredList_HidesWhatTheCallerCannotCall(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog, rooms.WithScopeFilteredList())
		reader := st.agentOn(t, wire, "alice", rooms.ScopeRoomsAttach, rooms.ScopePagesRead)

		res := listPage(t, reader)
		names := namesOf(res)
		assert.Contains(t, names, "get_location")
		assert.Contains(t, names, "list_pages")
		assert.NotContains(t, names, "show_sheet", "it needs pages:act, which this token lacks")
		assert.Equal(t, core.CacheScopePrivate, res.CacheScope)

		ups, ok := stepUpOf(t, reader)
		require.True(t, ok)
		assert.Equal(t, []stepUp{{Tool: "show_sheet", Scopes: []string{rooms.ScopePagesAct}}}, ups, "a hidden tool is still discoverable, with the scope to ask for")

		full := st.agentOn(t, wire, "bob", allScopes...)
		assert.Contains(t, namesOf(listPage(t, full)), "show_sheet")
		ups, ok = stepUpOf(t, full)
		require.True(t, ok)
		assert.Empty(t, ups)
	})
}

func TestScopeFilteredList_OffByDefault(t *testing.T) {
	st := newStack(t, catalog)
	reader := st.agentOn(t, "stateless", "alice", rooms.ScopeRoomsAttach, rooms.ScopePagesRead)
	res := listPage(t, reader)
	assert.Contains(t, namesOf(res), "show_sheet", "the list is the same for every caller unless the host asks")
	assert.NotEqual(t, core.CacheScopePrivate, res.CacheScope)
	_, ok := stepUpOf(t, reader)
	assert.False(t, ok)
}

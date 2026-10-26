package bridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/experimental/ext/events/clients/go/bridge"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type alert struct {
	Severity string `json:"severity"`
	Msg      string `json:"msg"`
}

type upstream struct {
	client   *client.Client
	yield    func(context.Context, alert) error
	webhooks *events.WebhookRegistry
}

// newUpstream stands up an events server with alert.fired (arguments
// constrained by an inputSchema, matched on severity) and build.finished.
func newUpstream(t *testing.T) upstream {
	t.Helper()
	alertSrc, yield := events.NewYieldingSource[alert](events.EventDef{
		Name:        "alert.fired",
		Description: "An alert fired on a monitored service",
		Delivery:    []string{"push", "poll", "webhook"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"severity": map[string]any{"type": "string", "enum": []any{"P1", "P2", "P3"}},
			},
			"additionalProperties": false,
		},
		Match: func(_ events.HookContext, ev events.Event, params map[string]any) bool {
			want, _ := params["severity"].(string)
			if want == "" {
				return true
			}
			var a alert
			_ = json.Unmarshal(ev.Data, &a)
			return a.Severity == want
		},
	})
	buildSrc, _ := events.NewYieldingSource[alert](events.EventDef{
		Name:        "build.finished",
		Description: "A CI build finished",
		Delivery:    []string{"poll", "webhook"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "upstream", Version: "0"}, server.WithSubscriptions())
	webhooks := events.NewWebhookRegistry(
		events.WithWebhookAllowPrivateNetworks(true),
		events.WithUnsafeWebhookAllowPlaintextCallbacks(),
	)
	events.Register(events.Config{
		Sources:                  []events.EventSource{alertSrc, buildSrc},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "bridge", Version: "0"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	return upstream{client: c, yield: yield, webhooks: webhooks}
}

type recordingSink chan eventsclient.Delivery

func (s recordingSink) Deliver(_ context.Context, d eventsclient.Delivery) error {
	s <- d
	return nil
}

// scopedSink is a recordingSink that reports a tool ceiling, as
// HeadlessClaudeSink does.
type scopedSink struct {
	recordingSink
	allowed, outward []string
}

func (s scopedSink) ToolCeiling() ([]string, []string) { return s.allowed, s.outward }

// hookServer is the bridge's public webhook endpoint. Its handler is swapped
// in after the bridge exists, since the bridge needs the URL first, and swapped
// again on a restart while the server may still be delivering.
type hookServer struct {
	ts *httptest.Server
	h  atomic.Value // http.Handler
}

func newHookServer(t *testing.T) *hookServer {
	hs := &hookServer{}
	hs.h.Store(http.NotFoundHandler())
	hs.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.h.Load().(http.Handler).ServeHTTP(w, r)
	}))
	t.Cleanup(hs.ts.Close)
	return hs
}

func newBridge(t *testing.T, hs *hookServer, sink eventsclient.EventSink, store bridge.Store, ups map[string]upstream) *bridge.Bridge {
	t.Helper()
	return newBridgeWith(t, hs, bridge.Config{Sink: sink, Store: store}, ups)
}

// newBridgeWith starts a bridge from cfg, filling in the hook URL, the
// upstreams and the test logger.
func newBridgeWith(t *testing.T, hs *hookServer, cfg bridge.Config, ups map[string]upstream) *bridge.Bridge {
	t.Helper()
	cfg.PublicURL, cfg.Logf = hs.ts.URL, t.Logf
	for name, u := range ups {
		cfg.Upstreams = append(cfg.Upstreams, bridge.Upstream{Name: name, Client: u.client})
	}
	b, err := bridge.New(cfg)
	require.NoError(t, err)
	hs.h.Store(b.Handler())
	require.NoError(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	return b
}

// agent connects an MCP client to a server carrying the bridge's tools.
func agent(t *testing.T, b *bridge.Bridge) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "events-bridge", Version: "0"})
	b.Register(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "agent", Version: "0"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func callTool(t *testing.T, c *client.Client, name string, args map[string]any) *core.ToolResult {
	t.Helper()
	res, err := c.ToolCallFull(t.Context(), name, args)
	require.NoError(t, err)
	return res
}

func structured[T any](t *testing.T, res *core.ToolResult) T {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func waitDelivery(t *testing.T, sink recordingSink) eventsclient.Delivery {
	t.Helper()
	select {
	case d := <-sink:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery reached the sink")
		return eventsclient.Delivery{}
	}
}

func assertNoDelivery(t *testing.T, sink recordingSink) {
	t.Helper()
	select {
	case d := <-sink:
		t.Fatalf("unexpected delivery: %+v", d)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestWatchList_QualifiesSortsAndFilters(t *testing.T) {
	ups := map[string]upstream{"wiki": newUpstream(t), "ci": newUpstream(t)}
	c := agent(t, newBridge(t, newHookServer(t), make(recordingSink, 1), nil, ups))

	type listOut struct {
		Events []bridge.CatalogEntry `json:"events"`
	}
	all := structured[listOut](t, callTool(t, c, "watch_list", nil))
	var names []string
	for _, e := range all.Events {
		names = append(names, e.Event)
	}
	assert.Equal(t, []string{
		"ci/alert.fired", "ci/build.finished", "ci/events.topology",
		"wiki/alert.fired", "wiki/build.finished", "wiki/events.topology",
	}, names)
	assert.NotNil(t, all.Events[0].InputSchema, "the schema watch arguments must match is part of the listing")

	one := structured[listOut](t, callTool(t, c, "watch_list", map[string]any{"server": "wiki", "query": "CI build"}))
	require.Len(t, one.Events, 1)
	assert.Equal(t, "wiki/build.finished", one.Events[0].Event)

	res := callTool(t, c, "watch_list", map[string]any{"server": "nope"})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "configured servers: ci, wiki")
}

func TestWatch_RefusesWithModelReadableErrors(t *testing.T) {
	c := agent(t, newBridge(t, newHookServer(t), make(recordingSink, 1), nil, map[string]upstream{"ops": newUpstream(t)}))

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unqualified", map[string]any{"event": "alert.fired", "instruction": "x"}, `use "<server>/<event>"`},
		{"unknown server", map[string]any{"event": "nope/alert.fired", "instruction": "x"}, "configured servers: ops"},
		{"unknown event", map[string]any{"event": "ops/nope", "instruction": "x"}, "call watch_list"},
		{"no instruction", map[string]any{"event": "ops/alert.fired"}, "missing property 'instruction'"},
		{"blank instruction", map[string]any{"event": "ops/alert.fired", "instruction": "  "}, "instruction is required"},
		{"bad enum", map[string]any{"event": "ops/alert.fired", "instruction": "x", "arguments": map[string]any{"severity": "P9"}}, "/severity"},
		{"extra property", map[string]any{"event": "ops/alert.fired", "instruction": "x", "arguments": map[string]any{"color": "red"}}, "inputSchema"},
		{"session id not a uuid", map[string]any{"event": "ops/alert.fired", "instruction": "x", "session": map[string]any{"id": "--dangerously-skip-permissions", "dir": "/tmp"}}, "$CLAUDE_CODE_SESSION_ID"},
		{"session dir relative", map[string]any{"event": "ops/alert.fired", "instruction": "x", "session": map[string]any{"id": "0b5c3e1a-7d2f-4c9e-8a61-2f4d9b7e1c35", "dir": "repo"}}, "absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, c, "watch", tc.args)
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].Text, tc.want)
		})
	}
	res := callTool(t, c, "watches", nil)
	assert.Equal(t, "0 watch(es)", res.Content[0].Text, "a refused watch leaves nothing behind")
}

func TestWatch_DeliversMatchingEventsWithInstruction(t *testing.T) {
	up := newUpstream(t)
	sink := make(recordingSink, 4)
	c := agent(t, newBridge(t, newHookServer(t), sink, nil, map[string]upstream{"ops": up}))

	res := callTool(t, c, "watch", map[string]any{
		"event":       "ops/alert.fired",
		"arguments":   map[string]any{"severity": "P1"},
		"instruction": "page me with a one-line summary",
	})
	require.False(t, res.IsError, res.Content[0].Text)
	view := structured[bridge.WatchView](t, res)
	raw, _ := json.Marshal(res.StructuredContent)
	assert.NotContains(t, string(raw), "whsec_", "the tools never show a watch's secret")

	require.NoError(t, up.yield(t.Context(), alert{Severity: "P3", Msg: "noise"}))
	require.NoError(t, up.yield(t.Context(), alert{Severity: "P1", Msg: "db down"}))

	d := waitDelivery(t, sink)
	assert.Equal(t, "ops/alert.fired", d.QualifiedName())
	assert.Equal(t, "page me with a one-line summary", d.Instruction)
	assert.Contains(t, string(d.Event.Data), "db down")
	assertNoDelivery(t, sink)

	w := structured[struct {
		Watches []bridge.WatchView `json:"watches"`
	}](t, callTool(t, c, "watches", nil))
	require.Len(t, w.Watches, 1)
	assert.Equal(t, view.ID, w.Watches[0].ID)
	assert.Equal(t, bridge.StatusActive, w.Watches[0].Status)
}

func TestWatch_SessionReachesTheSinkAndIsPersisted(t *testing.T) {
	up := newUpstream(t)
	sink := make(recordingSink, 4)
	store := bridge.NewMemoryStore()
	c := agent(t, newBridge(t, newHookServer(t), sink, store, map[string]upstream{"ops": up}))
	dir := t.TempDir()

	res := callTool(t, c, "watch", map[string]any{
		"event":       "ops/alert.fired",
		"instruction": "look at it with what we discussed",
		"session":     map[string]any{"id": "0b5c3e1a-7d2f-4c9e-8a61-2f4d9b7e1c35", "dir": dir},
	})
	require.False(t, res.IsError, res.Content[0].Text)
	view := structured[bridge.WatchView](t, res)
	require.NotNil(t, view.Session)
	assert.Equal(t, dir, view.Session.Dir)

	require.NoError(t, up.yield(t.Context(), alert{Severity: "P1", Msg: "db down"}))
	d := waitDelivery(t, sink)
	assert.Equal(t, "0b5c3e1a-7d2f-4c9e-8a61-2f4d9b7e1c35", d.Session)
	assert.Equal(t, dir, d.SessionDir)

	saved, err := store.Load()
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.NotNil(t, saved[0].Session, "the session survives a restart with the watch")
	assert.Equal(t, dir, saved[0].Session.Dir)
}

func TestWatches_SortedByID(t *testing.T) {
	b := newBridge(t, newHookServer(t), make(recordingSink, 8), nil, map[string]upstream{"ops": newUpstream(t)})
	for _, sev := range []string{"P1", "P2", "P3"} {
		_, err := b.Watch(t.Context(), "ops/alert.fired", map[string]any{"severity": sev}, "note it")
		require.NoError(t, err)
	}
	ids := []string{}
	for _, w := range b.List() {
		ids = append(ids, w.ID)
	}
	require.Len(t, ids, 3)
	assert.True(t, sort.StringsAreSorted(ids), "watches lists in id order: %v", ids)
}

func TestUnwatch_StopsDeliveryAndForgets(t *testing.T) {
	up := newUpstream(t)
	sink := make(recordingSink, 4)
	store := bridge.NewMemoryStore()
	b := newBridge(t, newHookServer(t), sink, store, map[string]upstream{"ops": up})

	w, err := b.Watch(t.Context(), "ops/alert.fired", nil, "note it")
	require.NoError(t, err)
	require.NoError(t, b.Unwatch(t.Context(), w.ID))

	require.NoError(t, up.yield(t.Context(), alert{Severity: "P1", Msg: "after unwatch"}))
	assertNoDelivery(t, sink)
	assert.Empty(t, b.List())
	saved, _ := store.Load()
	assert.Empty(t, saved)

	var werr *bridge.WatchError
	assert.ErrorAs(t, b.Unwatch(t.Context(), w.ID), &werr)
}

func TestUnwatch_RemovesTheUpstreamSubscription(t *testing.T) {
	up := newUpstream(t)
	b := newBridge(t, newHookServer(t), make(recordingSink, 4), nil, map[string]upstream{"ops": up})

	w, err := b.Watch(t.Context(), "ops/alert.fired", nil, "note it")
	require.NoError(t, err)
	require.Len(t, up.webhooks.Targets(), 1)
	require.NoError(t, b.Unwatch(t.Context(), w.ID))
	assert.Empty(t, up.webhooks.Targets(), "the server no longer holds the subscription")
}

func TestWatchList_ReportsTheToolCeiling(t *testing.T) {
	ups := map[string]upstream{"ops": newUpstream(t)}
	sink := scopedSink{recordingSink: make(recordingSink, 1), allowed: []string{"Read", "Grep"}, outward: []string{"mcp__gmail__send"}}
	res := callTool(t, agent(t, newBridge(t, newHookServer(t), sink, nil, ups)), "watch_list", nil)

	out := structured[struct {
		Tools struct{ Allowed, Outward []string } `json:"tools"`
	}](t, res)
	assert.Equal(t, []string{"Grep", "Read"}, out.Tools.Allowed)
	assert.Equal(t, []string{"mcp__gmail__send"}, out.Tools.Outward)
	assert.Contains(t, res.Content[0].Text, "A watch's tools may name: Grep, Read, mcp__gmail__send")

	plain := callTool(t, agent(t, newBridge(t, newHookServer(t), make(recordingSink, 1), nil, ups)), "watch_list", nil)
	raw, _ := json.Marshal(plain.StructuredContent)
	assert.NotContains(t, string(raw), `"tools"`, "a sink without a tool list reports none")
}

// A standing rule outlives the bridge: a second bridge on the same store
// resubscribes with the stored secret and cursor, and an event yielded while
// the first was down is delivered from that cursor.
func TestBridge_ResumesWatchesAcrossRestart(t *testing.T) {
	up := newUpstream(t)
	path := filepath.Join(t.TempDir(), "watches.json")
	hs := newHookServer(t)

	sink1 := make(recordingSink, 4)
	b1 := newBridge(t, hs, sink1, bridge.NewFileStore(path), map[string]upstream{"ops": up})
	w, err := b1.Watch(t.Context(), "ops/alert.fired", nil, "summarize it")
	require.NoError(t, err)
	require.NoError(t, up.yield(t.Context(), alert{Severity: "P2", Msg: "first"}))
	assert.Contains(t, string(waitDelivery(t, sink1).Event.Data), "first")
	b1.Close()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the store holds webhook secrets")
	saved, err := bridge.NewFileStore(path).Load()
	require.NoError(t, err)
	require.Len(t, saved, 1)
	require.NotNil(t, saved[0].Cursor, "the cursor after the delivered event is persisted")

	require.NoError(t, up.yield(t.Context(), alert{Severity: "P2", Msg: "while down"}))

	sink2 := make(recordingSink, 4)
	b2 := newBridge(t, hs, sink2, bridge.NewFileStore(path), map[string]upstream{"ops": up})
	d := waitDelivery(t, sink2)
	assert.Contains(t, string(d.Event.Data), "while down")
	assert.Equal(t, "summarize it", d.Instruction)
	assertNoDelivery(t, sink2)

	got := b2.List()
	require.Len(t, got, 1)
	assert.Equal(t, w.ID, got[0].ID)
	assert.Equal(t, w.Secret, got[0].Secret, "a restart refreshes the same subscription")
}

func TestBridge_ResumeWithUnknownServerKeepsWatchWithError(t *testing.T) {
	store := bridge.NewMemoryStore()
	require.NoError(t, store.Save([]bridge.Watch{{
		ID: "w_old", Event: "gone/alert.fired", Server: "gone", Name: "alert.fired",
		Instruction: "x", Secret: events.GenerateSecret(), Status: bridge.StatusActive,
	}}))
	b := newBridge(t, newHookServer(t), make(recordingSink, 1), store, map[string]upstream{"ops": newUpstream(t)})
	got := b.List()
	require.Len(t, got, 1)
	assert.Equal(t, bridge.StatusActive, got[0].Status)
	assert.True(t, strings.Contains(got[0].LastError, `server "gone" is not configured`), got[0].LastError)
}

func TestFileStore_MissingFileIsEmpty(t *testing.T) {
	ws, err := bridge.NewFileStore(filepath.Join(t.TempDir(), "absent.json")).Load()
	require.NoError(t, err)
	assert.Empty(t, ws)
}

func TestNew_RejectsBadConfig(t *testing.T) {
	c := newUpstream(t).client
	sink := make(recordingSink)
	for _, cfg := range []bridge.Config{
		{PublicURL: "http://x", Sink: sink},
		{PublicURL: "http://x", Upstreams: []bridge.Upstream{{Name: "a", Client: c}}},
		{Sink: sink, Upstreams: []bridge.Upstream{{Name: "a", Client: c}}},
		{PublicURL: "http://x", Sink: sink, Upstreams: []bridge.Upstream{{Name: "a/b", Client: c}}},
		{PublicURL: "http://x", Sink: sink, Upstreams: []bridge.Upstream{{Name: "a", Client: c}, {Name: "a", Client: c}}},
	} {
		_, err := bridge.New(cfg)
		assert.Error(t, err)
	}
}

func TestWatch_ToolsAreCheckedAgainstTheSinkCeiling(t *testing.T) {
	sink := scopedSink{recordingSink: make(recordingSink, 1), allowed: []string{"Read", "Grep"}, outward: []string{"mcp__gmail__send"}}
	c := agent(t, newBridge(t, newHookServer(t), sink, nil, map[string]upstream{"ops": newUpstream(t)}))

	for _, tc := range []struct {
		name  string
		tools []any
		want  string
	}{
		{"unknown tool", []any{"Read", "Bash"}, "tools Bash are not offered by this bridge; a watch may use only: Grep, Read, mcp__gmail__send"},
		{"empty list", []any{}, "tools is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, c, "watch", map[string]any{"event": "ops/alert.fired", "instruction": "x", "tools": tc.tools})
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].Text, tc.want)
		})
	}
	assert.Equal(t, "0 watch(es)", callTool(t, c, "watches", nil).Content[0].Text)
}

func TestWatch_ToolsRefusedWhenTheSinkHasNoCeiling(t *testing.T) {
	c := agent(t, newBridge(t, newHookServer(t), make(recordingSink, 1), nil, map[string]upstream{"ops": newUpstream(t)}))
	res := callTool(t, c, "watch", map[string]any{"event": "ops/alert.fired", "instruction": "x", "tools": []any{"Read"}})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "takes no per-watch tool list")
}

func TestWatch_ToolsNarrowTheViewPersistAndReachTheSink(t *testing.T) {
	up := newUpstream(t)
	sink := scopedSink{recordingSink: make(recordingSink, 4), allowed: []string{"Read", "Grep"}, outward: []string{"mcp__gmail__send", "mcp__slack__post"}}
	store := bridge.NewMemoryStore()
	c := agent(t, newBridge(t, newHookServer(t), sink, store, map[string]upstream{"ops": up}))

	res := callTool(t, c, "watch", map[string]any{
		"event": "ops/alert.fired", "instruction": "post it to the channel",
		"tools": []any{"mcp__slack__post", "Read", "Read"},
	})
	require.False(t, res.IsError, res.Content[0].Text)
	view := structured[bridge.WatchView](t, res)
	assert.Equal(t, []string{"Read", "mcp__slack__post"}, view.Tools)
	assert.Equal(t, []string{"mcp__slack__post"}, view.OutwardTools)

	saved, err := store.Load()
	require.NoError(t, err)
	require.Len(t, saved, 1)
	assert.Equal(t, []string{"Read", "mcp__slack__post"}, saved[0].Tools, "sorted and deduplicated")

	require.NoError(t, up.yield(t.Context(), alert{Severity: "P1", Msg: "db down"}))
	d := waitDelivery(t, sink.recordingSink)
	assert.Equal(t, []string{"Read", "mcp__slack__post"}, d.Tools)
	assert.NotEmpty(t, d.SubscriptionID, "the webhook's X-MCP-Subscription-Id reaches the sink")
}

// A watch that names no tools shows everything the sink offers, so the user
// sees what the rule can use either way.
func TestWatch_ViewWithoutToolsShowsTheCeiling(t *testing.T) {
	sink := scopedSink{recordingSink: make(recordingSink, 1), allowed: []string{"Read"}, outward: []string{"mcp__gmail__send"}}
	c := agent(t, newBridge(t, newHookServer(t), sink, nil, map[string]upstream{"ops": newUpstream(t)}))
	view := structured[bridge.WatchView](t, callTool(t, c, "watch", map[string]any{"event": "ops/alert.fired", "instruction": "x"}))
	assert.Equal(t, []string{"Read", "mcp__gmail__send"}, view.Tools)
	assert.Equal(t, []string{"mcp__gmail__send"}, view.OutwardTools)
}

// gatedSink holds every delivery until open is closed, the way a run waiting
// on a PendingApprover holds the watch's delivery loop.
type gatedSink struct {
	open chan struct{}
	mu   sync.Mutex
	ids  map[string]bool
}

func (s *gatedSink) Deliver(ctx context.Context, d eventsclient.Delivery) error {
	select {
	case <-s.open:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.ids[d.Event.EventID] = true
	s.mu.Unlock()
	return nil
}

func (s *gatedSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

// A burst that arrives while the sink is blocked is queued per watch and
// delivered once the sink frees up, rather than dropped by the receiver
// (#1603).
func TestWatch_BurstWhileTheSinkIsBlockedIsNotLost(t *testing.T) {
	up := newUpstream(t)
	sink := &gatedSink{open: make(chan struct{}), ids: map[string]bool{}}
	c := agent(t, newBridge(t, newHookServer(t), sink, nil, map[string]upstream{"ops": up}))
	res := callTool(t, c, "watch", map[string]any{"event": "ops/alert.fired", "instruction": "triage"})
	require.False(t, res.IsError, res.Content[0].Text)

	const burst = 200
	for i := range burst {
		require.NoError(t, up.yield(t.Context(), alert{Severity: "P1", Msg: strconv.Itoa(i)}))
	}
	time.Sleep(time.Second)
	close(sink.open)
	require.Eventually(t, func() bool { return sink.count() == burst }, 10*time.Second, 20*time.Millisecond,
		"every event of the burst reaches the sink")
}

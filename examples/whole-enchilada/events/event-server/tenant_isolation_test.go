package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTenantValidator stamps Claims.Tenant from the Bearer-token
// value. The token IS the tenant name in these tests — keeps the
// fixture small while letting us exercise the real tenant-encoding
// path (resolvePrincipal → PrincipalFor → "<tenant>/<subject>") that
// the production introspection validator drives.
type fakeTenantValidator struct{}

func (fakeTenantValidator) Validate(r *http.Request) error {
	if extractTestTenant(r) == "" {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "missing tenant token"}
	}
	return nil
}

func (fakeTenantValidator) Claims(r *http.Request) *core.Claims {
	tenant := extractTestTenant(r)
	if tenant == "" {
		return nil
	}
	return &core.Claims{Subject: "user-of-" + tenant, Tenant: tenant}
}

func extractTestTenant(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return h[len(prefix):]
}

// buildTenantTestStack mirrors buildTestStack but wires the
// fakeTenantValidator AND the tenant-aware MatchFunc onto every
// EventDef so subscribers' Claims.Tenant gates delivery.
func buildTenantTestStack(t *testing.T) (*httptest.Server, *events.HTTPSource[ChatMessageData], *events.HTTPSource[PresenceChangedData], *events.WebhookRegistry) {
	t.Helper()

	chatSrc := events.NewHTTPSource[ChatMessageData](events.EventDef{
		Name:     "chat.message",
		Delivery: []string{"push", "poll", "webhook"},
		Match:    tenantMatchFunc,
	}, events.HTTPSourceConfig{
		YieldingOpts: []events.YieldingOption{events.WithMaxSize(100)},
	})
	presenceSrc := events.NewHTTPSource[PresenceChangedData](events.EventDef{
		Name:     "presence.changed",
		Delivery: []string{"push", "webhook"},
		Match:    tenantMatchFunc,
	}, events.HTTPSourceConfig{
		YieldingOpts: []events.YieldingOption{events.WithoutCursors()},
	})

	webhooks := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))

	srv := server.NewServer(
		core.ServerInfo{Name: "whole-enchilada-tenant-test", Version: "0.1.0"},
		server.WithSubscriptions(),
		server.WithAuth(fakeTenantValidator{}),
	)
	registerResources(srv, chatSrc)
	events.Register(events.Config{
		Sources:  []events.EventSource{chatSrc, presenceSrc},
		Webhooks: webhooks,
		Server:   srv,
	})

	mcpHandler := srv.Handler(server.WithStreamableHTTP(true))
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle(chatSrc.InjectPath(), chatSrc.Handler())
	mux.Handle(presenceSrc.InjectPath(), presenceSrc.Handler())

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, chatSrc, presenceSrc, webhooks
}

func connectClientAsTenant(t *testing.T, ts *httptest.Server, tenant string) *client.Client {
	t.Helper()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-" + tenant, Version: "1.0"},
		client.WithClientBearerToken(tenant),
	)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestTenantMatchFunc_TableDriven verifies the pure-function rule the
// MatchFunc enforces. Decouples the rule from the server wiring.
func TestTenantMatchFunc_TableDriven(t *testing.T) {
	cases := []struct {
		name      string
		principal string
		payload   any
		want      bool
	}{
		{"untagged event delivers to anyone", "tenant-a/alice", ChatMessageData{Sender: "x"}, true},
		{"matched tenant delivers", "tenant-a/alice", ChatMessageData{Tenant: "tenant-a", Sender: "x"}, true},
		{"mismatched tenant drops", "tenant-a/alice", ChatMessageData{Tenant: "tenant-b", Sender: "x"}, false},
		{"empty subscriber tenant + tagged event drops", "alice", ChatMessageData{Tenant: "tenant-a", Sender: "x"}, false},
		{"empty subscriber tenant + untagged event delivers", "alice", ChatMessageData{Sender: "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)
			event := events.Event{Name: "chat.message", Data: body}
			ctx := &fakeHookContext{principal: tc.principal}
			got := tenantMatchFunc(ctx, event, nil)
			assert.Equal(t, tc.want, got)
		})
	}
}

// fakeHookContext is the smallest HookContext we can pass to
// tenantMatchFunc — only Principal() matters for the test.
type fakeHookContext struct {
	principal string
}

func (f *fakeHookContext) Context() context.Context  { return context.Background() }
func (f *fakeHookContext) Principal() string         { return f.principal }
func (f *fakeHookContext) SubscriptionID() string    { return "" }
func (f *fakeHookContext) Mode() events.DeliveryMode { return events.DeliveryModePush }

// TestE2E_TenantIsolation_Poll asserts the core stage-2 contract: a
// poll subscriber from tenant-a only sees events tagged for tenant-a;
// the same poll from tenant-b sees only tenant-b events. End-to-end
// through the MCP wire, not just the MatchFunc in isolation.
func TestE2E_TenantIsolation_Poll(t *testing.T) {
	ts, _, _, _ := buildTenantTestStack(t)
	cA := connectClientAsTenant(t, ts, "tenant-a")
	cB := connectClientAsTenant(t, ts, "tenant-b")

	pusher := eventsclient.NewPusher(ts.URL, "")
	ctx := context.Background()

	// Push three events with rotating tenant tags. Subscribers from
	// each tenant should see exactly one event after polling.
	require.NoError(t, pusher.PushNamed(ctx, "chat.message", ChatMessageData{
		Tenant: "tenant-a", Channel: "general", Sender: "alice", Text: "hello A",
	}))
	require.NoError(t, pusher.PushNamed(ctx, "chat.message", ChatMessageData{
		Tenant: "tenant-b", Channel: "general", Sender: "bob", Text: "hello B",
	}))
	require.NoError(t, pusher.PushNamed(ctx, "chat.message", ChatMessageData{
		Tenant: "tenant-c", Channel: "general", Sender: "carol", Text: "hello C",
	}))

	pollAs := func(c *client.Client) []events.Event {
		raw, err := c.Call("events/poll", map[string]any{"name": "chat.message", "cursor": "0"})
		require.NoError(t, err)
		var pr pollResultWire
		require.NoError(t, json.Unmarshal(raw.Raw, &pr))
		return pr.Events
	}

	eventsA := pollAs(cA)
	require.Len(t, eventsA, 1, "tenant-a must see exactly one event (the tenant-a-tagged one)")
	var dataA ChatMessageData
	require.NoError(t, json.Unmarshal(eventsA[0].Data, &dataA))
	assert.Equal(t, "tenant-a", dataA.Tenant)
	assert.Equal(t, "hello A", dataA.Text)

	eventsB := pollAs(cB)
	require.Len(t, eventsB, 1, "tenant-b must see exactly one event (the tenant-b-tagged one)")
	var dataB ChatMessageData
	require.NoError(t, json.Unmarshal(eventsB[0].Data, &dataB))
	assert.Equal(t, "tenant-b", dataB.Tenant)
}

// TestE2E_TenantIsolation_UntaggedEventReachesAll asserts that events
// emitted without a tenant tag — the stage-1 path, or any tenant-naive
// upstream — deliver to every subscriber regardless of their claims.
// This preserves backwards compatibility with single-tenant deployments.
func TestE2E_TenantIsolation_UntaggedEventReachesAll(t *testing.T) {
	ts, _, _, _ := buildTenantTestStack(t)
	cA := connectClientAsTenant(t, ts, "tenant-a")
	cB := connectClientAsTenant(t, ts, "tenant-b")

	pusher := eventsclient.NewPusher(ts.URL, "")
	require.NoError(t, pusher.PushNamed(context.Background(), "chat.message", ChatMessageData{
		Channel: "general", Sender: "x", Text: "untagged", // Tenant left empty
	}))

	pollAs := func(c *client.Client) []events.Event {
		raw, err := c.Call("events/poll", map[string]any{"name": "chat.message", "cursor": "0"})
		require.NoError(t, err)
		var pr pollResultWire
		require.NoError(t, json.Unmarshal(raw.Raw, &pr))
		return pr.Events
	}

	require.Len(t, pollAs(cA), 1, "untagged event must reach tenant-a")
	require.Len(t, pollAs(cB), 1, "untagged event must reach tenant-b")
}

// TestE2E_TenantIsolation_CrossTenantPollDoesNotLeak runs the two
// tenants in parallel after the events are interleaved on the wire,
// and asserts each tenant's poll only retrieves its own subset. This
// is the explicit cross-tenant non-interference assertion the WG
// announcement will cite.
func TestE2E_TenantIsolation_CrossTenantPollDoesNotLeak(t *testing.T) {
	ts, _, _, _ := buildTenantTestStack(t)
	cA := connectClientAsTenant(t, ts, "tenant-a")
	cB := connectClientAsTenant(t, ts, "tenant-b")

	pusher := eventsclient.NewPusher(ts.URL, "")
	ctx := context.Background()

	// 6 events: 3 for A, 3 for B, interleaved.
	for i := 0; i < 3; i++ {
		require.NoError(t, pusher.PushNamed(ctx, "chat.message", ChatMessageData{
			Tenant: "tenant-a", Channel: "g", Sender: "alice", Text: "A",
		}))
		require.NoError(t, pusher.PushNamed(ctx, "chat.message", ChatMessageData{
			Tenant: "tenant-b", Channel: "g", Sender: "bob", Text: "B",
		}))
	}

	pollAs := func(c *client.Client) []events.Event {
		raw, err := c.Call("events/poll", map[string]any{"name": "chat.message", "cursor": "0"})
		require.NoError(t, err)
		var pr pollResultWire
		require.NoError(t, json.Unmarshal(raw.Raw, &pr))
		return pr.Events
	}

	eventsA := pollAs(cA)
	require.Len(t, eventsA, 3, "tenant-a sees only its 3 events")
	for i, ev := range eventsA {
		var data ChatMessageData
		require.NoError(t, json.Unmarshal(ev.Data, &data))
		assert.Equal(t, "tenant-a", data.Tenant, "event %d tenant", i)
	}

	eventsB := pollAs(cB)
	require.Len(t, eventsB, 3, "tenant-b sees only its 3 events")
	for i, ev := range eventsB {
		var data ChatMessageData
		require.NoError(t, json.Unmarshal(ev.Data, &data))
		assert.Equal(t, "tenant-b", data.Tenant, "event %d tenant", i)
	}
}

package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"golang.org/x/time/rate"
)

// newSubscriptionTestSession returns an initialized dispatcher attached to
// srv with the chosen sessionID. Wraps the same wiring testSubscriptionDispatcher
// uses but lets the cap tests stand up multiple sessions on one server.
func newSubscriptionTestSession(srv *Server, sessionID string) *Dispatcher {
	d := srv.newSession()
	d.sessionID = sessionID
	initDispatcher(d)
	return d
}

// subscribeReq builds a resources/subscribe JSON-RPC request for uri with
// the named request id. Keeps each test compact.
func subscribeReq(id int, uri string) *core.Request {
	idRaw, _ := json.Marshal(id)
	params, _ := json.Marshal(struct {
		URI string `json:"uri"`
	}{URI: uri})
	return &core.Request{JSONRPC: "2.0", ID: idRaw, Method: "resources/subscribe", Params: core.NewRawJSON(params)}
}

func unsubscribeReq(id int, uri string) *core.Request {
	idRaw, _ := json.Marshal(id)
	params, _ := json.Marshal(struct {
		URI string `json:"uri"`
	}{URI: uri})
	return &core.Request{JSONRPC: "2.0", ID: idRaw, Method: "resources/unsubscribe", Params: core.NewRawJSON(params)}
}

func makeCapTestServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "cap-test", Version: "1.0"},
		append([]Option{WithSubscriptions()}, opts...)...,
	)
	// Three subscribable URIs is enough for cap=2 and cap=200 cases.
	for _, uri := range []string{"test://a", "test://b", "test://c", "test://d"} {
		uri := uri
		srv.RegisterResource(
			core.ResourceDef{URI: uri, Name: uri, MimeType: "text/plain"},
			func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
				return core.ResourceResult{}, nil
			},
		)
	}
	return srv
}

func TestSubscriptionCap_RejectsOverLimit(t *testing.T) {
	srv := makeCapTestServer(t, WithSubscriptionCap(2))
	d := newSubscriptionTestSession(srv, "session-A")
	ctx := context.Background()

	for i, uri := range []string{"test://a", "test://b"} {
		resp := d.Dispatch(ctx, subscribeReq(i+1, uri))
		if resp.Error != nil {
			t.Fatalf("subscribe %d (under cap) failed: %s", i+1, resp.Error.Message)
		}
	}
	resp := d.Dispatch(ctx, subscribeReq(3, "test://c"))
	if resp.Error == nil {
		t.Fatal("subscribe over cap: want error, got nil")
	}
	if resp.Error.Code != core.ErrCodeSubscriptionLimitExceeded {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeSubscriptionLimitExceeded)
	}
	if data, ok := resp.Error.Data.(map[string]any); ok {
		if data["reason"] != "cap_exceeded" {
			t.Errorf("error.data.reason = %v, want cap_exceeded", data["reason"])
		}
	} else {
		t.Errorf("error.data has unexpected shape: %#v", resp.Error.Data)
	}
}

func TestSubscriptionCap_AllowsAfterUnsubscribe(t *testing.T) {
	srv := makeCapTestServer(t, WithSubscriptionCap(2))
	d := newSubscriptionTestSession(srv, "session-A")
	ctx := context.Background()

	d.Dispatch(ctx, subscribeReq(1, "test://a"))
	d.Dispatch(ctx, subscribeReq(2, "test://b"))
	resp := d.Dispatch(ctx, unsubscribeReq(3, "test://a"))
	if resp.Error != nil {
		t.Fatalf("unsubscribe failed: %s", resp.Error.Message)
	}
	resp = d.Dispatch(ctx, subscribeReq(4, "test://c"))
	if resp.Error != nil {
		t.Fatalf("subscribe after unsubscribe under cap: want nil error, got %s", resp.Error.Message)
	}
}

func TestSubscriptionCap_PerSession(t *testing.T) {
	srv := makeCapTestServer(t, WithSubscriptionCap(2))
	a := newSubscriptionTestSession(srv, "session-A")
	b := newSubscriptionTestSession(srv, "session-B")
	ctx := context.Background()

	for _, sess := range []*Dispatcher{a, b} {
		for i, uri := range []string{"test://a", "test://b"} {
			resp := sess.Dispatch(ctx, subscribeReq(i+1, uri))
			if resp.Error != nil {
				t.Fatalf("session %s subscribe %d failed: %s", sess.sessionID, i+1, resp.Error.Message)
			}
		}
	}
	// Each session is at its own cap of 2. A third subscribe on either
	// would be refused, but only after they each got their own 2.
	resp := a.Dispatch(ctx, subscribeReq(99, "test://c"))
	if resp.Error == nil || resp.Error.Code != core.ErrCodeSubscriptionLimitExceeded {
		t.Errorf("session-A third subscribe: want cap-exceeded, got %#v", resp.Error)
	}
}

func TestSubscriptionCap_UnlimitedByDefault(t *testing.T) {
	srv := makeCapTestServer(t) // no cap opt
	d := newSubscriptionTestSession(srv, "session-A")
	ctx := context.Background()

	// 4 known URIs registered; subscribe each once.
	for i, uri := range []string{"test://a", "test://b", "test://c", "test://d"} {
		resp := d.Dispatch(ctx, subscribeReq(i+1, uri))
		if resp.Error != nil {
			t.Fatalf("default cap should be unlimited; subscribe %d failed: %s", i+1, resp.Error.Message)
		}
	}
}

func TestSubscriptionRateLimit_BurstHonored(t *testing.T) {
	// 1 token/second steady state, burst 2 — so three rapid subs admit
	// two and refuse the third.
	srv := makeCapTestServer(t, WithSubscriptionRateLimit(rate.Limit(1), 2))
	d := newSubscriptionTestSession(srv, "session-A")
	ctx := context.Background()

	results := make([]*core.Response, 0, 3)
	for i, uri := range []string{"test://a", "test://b", "test://c"} {
		results = append(results, d.Dispatch(ctx, subscribeReq(i+1, uri)))
	}
	if results[0].Error != nil || results[1].Error != nil {
		t.Fatalf("first two subscribes (within burst) should succeed; got %v / %v", results[0].Error, results[1].Error)
	}
	if results[2].Error == nil {
		t.Fatal("third rapid subscribe: want rate-limited error, got nil")
	}
	if results[2].Error.Code != core.ErrCodeSubscriptionLimitExceeded {
		t.Errorf("error code = %d, want %d", results[2].Error.Code, core.ErrCodeSubscriptionLimitExceeded)
	}
	if data, ok := results[2].Error.Data.(map[string]any); ok {
		if data["reason"] != "rate_limited" {
			t.Errorf("error.data.reason = %v, want rate_limited", data["reason"])
		}
	}
}

func TestSubscriptionRejectHook_Fires(t *testing.T) {
	type rejection struct{ sessionID, uri, reason string }
	var (
		mu        sync.Mutex
		rejections []rejection
	)
	hook := func(sessionID, uri, reason string) {
		mu.Lock()
		defer mu.Unlock()
		rejections = append(rejections, rejection{sessionID, uri, reason})
	}

	srv := makeCapTestServer(t, WithSubscriptionCap(1), WithSubscriptionRejectHook(hook))
	d := newSubscriptionTestSession(srv, "session-A")
	ctx := context.Background()

	if resp := d.Dispatch(ctx, subscribeReq(1, "test://a")); resp.Error != nil {
		t.Fatalf("under-cap subscribe failed: %s", resp.Error.Message)
	}
	if resp := d.Dispatch(ctx, subscribeReq(2, "test://b")); resp.Error == nil {
		t.Fatal("over-cap subscribe: want error, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rejections) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(rejections))
	}
	r := rejections[0]
	if r.sessionID != "session-A" || r.uri != "test://b" || r.reason != "cap_exceeded" {
		t.Errorf("hook received %+v, want {session-A test://b cap_exceeded}", r)
	}
}

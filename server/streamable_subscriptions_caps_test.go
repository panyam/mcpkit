package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
	"golang.org/x/time/rate"
)

// --- Registry-level tests ----------------------------------------------------
//
// These exercise tryRegister + unregister directly against
// statelessSubMap. They're the fast path for cap, rate-limit, and
// per-scope semantics: no HTTP, no goroutines, no SSE timing. Two
// end-to-end tests below cover the wire shape (HTTP 429 + JSON
// error.data) but only after the registry behavior is proven here.

func newTestSub(id string) *statelessSubscriber {
	return &statelessSubscriber{
		id:     id,
		frames: make(chan stateless.TaggedFrame, 1),
		done:   make(chan struct{}),
	}
}

func TestStatelessSubMap_CapRejectsOverLimit(t *testing.T) {
	m := newStatelessSubMap(2, 0, 0, nil, DefaultStatelessSubscriptionScope)

	if err := m.tryRegister(newTestSub("a"), "scope-A"); err != nil {
		t.Fatalf("first under cap: %v", err)
	}
	if err := m.tryRegister(newTestSub("b"), "scope-A"); err != nil {
		t.Fatalf("second under cap: %v", err)
	}
	err := m.tryRegister(newTestSub("c"), "scope-A")
	if !errors.Is(err, ErrStatelessStreamCapExceeded) {
		t.Fatalf("third over cap: err = %v, want ErrStatelessStreamCapExceeded", err)
	}
}

func TestStatelessSubMap_CapAllowsAfterUnregister(t *testing.T) {
	m := newStatelessSubMap(2, 0, 0, nil, DefaultStatelessSubscriptionScope)

	_ = m.tryRegister(newTestSub("a"), "scope-A")
	_ = m.tryRegister(newTestSub("b"), "scope-A")
	m.unregister("a")

	if err := m.tryRegister(newTestSub("c"), "scope-A"); err != nil {
		t.Errorf("subscribe after unregister under cap: %v", err)
	}
}

func TestStatelessSubMap_CapPerScope(t *testing.T) {
	m := newStatelessSubMap(2, 0, 0, nil, DefaultStatelessSubscriptionScope)

	for _, scope := range []string{"scope-A", "scope-B"} {
		for i, id := range []string{"x1", "x2"} {
			err := m.tryRegister(newTestSub(scope+id), scope)
			if err != nil {
				t.Errorf("scope %q stream %d: %v", scope, i+1, err)
			}
		}
	}
	// Third on scope-A only — scope-B remains unaffected.
	if err := m.tryRegister(newTestSub("ax3"), "scope-A"); !errors.Is(err, ErrStatelessStreamCapExceeded) {
		t.Errorf("scope-A third stream: err = %v, want cap exceeded", err)
	}
}

func TestStatelessSubMap_CapUnlimitedByDefault(t *testing.T) {
	m := newStatelessSubMap(0, 0, 0, nil, DefaultStatelessSubscriptionScope)
	for i := 0; i < 50; i++ {
		if err := m.tryRegister(newTestSub(string(rune('a'+i%26))+string(rune('0'+i/26))), "shared"); err != nil {
			t.Fatalf("unlimited default: subscribe %d failed: %v", i, err)
		}
	}
}

func TestStatelessSubMap_RateLimitBurstHonored(t *testing.T) {
	m := newStatelessSubMap(0, rate.Limit(1), 2, nil, DefaultStatelessSubscriptionScope)

	if err := m.tryRegister(newTestSub("a"), "scope-A"); err != nil {
		t.Fatalf("burst 1: %v", err)
	}
	if err := m.tryRegister(newTestSub("b"), "scope-A"); err != nil {
		t.Fatalf("burst 2: %v", err)
	}
	err := m.tryRegister(newTestSub("c"), "scope-A")
	if !errors.Is(err, ErrStatelessStreamRateLimited) {
		t.Fatalf("burst 3: err = %v, want rate-limited", err)
	}
}

func TestStatelessSubMap_RejectHookFires(t *testing.T) {
	type rejection struct{ scope, target, reason string }
	var (
		mu         sync.Mutex
		rejections []rejection
	)
	hook := func(scope, target, reason string) {
		mu.Lock()
		defer mu.Unlock()
		rejections = append(rejections, rejection{scope, target, reason})
	}
	m := newStatelessSubMap(1, 0, 0, hook, DefaultStatelessSubscriptionScope)

	_ = m.tryRegister(newTestSub("a"), "scope-A")
	if err := m.tryRegister(newTestSub("b"), "scope-A"); err == nil {
		t.Fatal("second over cap: want error, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rejections) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(rejections))
	}
	got := rejections[0]
	if got.scope != "scope-A" || got.target != "subscriptions/listen" || got.reason != "cap_exceeded" {
		t.Errorf("hook payload = %+v, want {scope-A subscriptions/listen cap_exceeded}", got)
	}
}

func TestStatelessSubMap_UnregisterDropsLimiterOnZero(t *testing.T) {
	m := newStatelessSubMap(0, rate.Limit(10), 1, nil, DefaultStatelessSubscriptionScope)
	if err := m.tryRegister(newTestSub("a"), "scope-A"); err != nil {
		t.Fatalf("register: %v", err)
	}
	m.mu.RLock()
	if _, ok := m.limitersByScope["scope-A"]; !ok {
		m.mu.RUnlock()
		t.Fatal("limiter not created on first register")
	}
	m.mu.RUnlock()

	m.unregister("a")
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.limitersByScope["scope-A"]; ok {
		t.Error("limiter not dropped when scope count hit zero")
	}
	if _, ok := m.countsByScope["scope-A"]; ok {
		t.Error("count entry not dropped when scope count hit zero")
	}
}

func TestEffectiveStatelessSubCap(t *testing.T) {
	// Zero option value picks up the default — deployers who never call
	// WithStatelessSubscriptionCap get defense out of the box.
	if got := effectiveStatelessSubCap(0); got != DefaultStatelessSubscriptionCap {
		t.Errorf("effectiveStatelessSubCap(0) = %d, want DefaultStatelessSubscriptionCap (%d)", got, DefaultStatelessSubscriptionCap)
	}
	// Explicit negative disables the cap — registry treats 0 as unlimited.
	if got := effectiveStatelessSubCap(-1); got != 0 {
		t.Errorf("effectiveStatelessSubCap(-1) = %d, want 0 (unlimited)", got)
	}
	// Positive passes through unchanged.
	if got := effectiveStatelessSubCap(42); got != 42 {
		t.Errorf("effectiveStatelessSubCap(42) = %d, want 42", got)
	}
}

func TestDefaultStatelessSubscriptionScope_RemoteAddrHost(t *testing.T) {
	r := &http.Request{RemoteAddr: "203.0.113.5:54321"}
	if got := DefaultStatelessSubscriptionScope(r); got != "203.0.113.5" {
		t.Errorf("DefaultStatelessSubscriptionScope = %q, want 203.0.113.5", got)
	}

	// Untrusted X-Forwarded-For MUST be ignored by the default.
	r2 := &http.Request{
		RemoteAddr: "203.0.113.5:54321",
		Header:     http.Header{"X-Forwarded-For": {"198.51.100.7"}},
	}
	if got := DefaultStatelessSubscriptionScope(r2); got != "203.0.113.5" {
		t.Errorf("default scope must not consult X-Forwarded-For; got %q", got)
	}

	r3 := &http.Request{RemoteAddr: "bare-host"}
	if got := DefaultStatelessSubscriptionScope(r3); got != "bare-host" {
		t.Errorf("bare RemoteAddr fallback: got %q", got)
	}
}

// --- Wire-level end-to-end --------------------------------------------------
//
// The two tests below confirm the registry plumbing surfaces correctly
// on the SEP-2575 wire: HTTP 429 status, JSON-RPC -32010 in the body,
// reason field populated. Single happy path + single reject path is
// sufficient since registry-level tests already cover all semantics.

func newStatelessTestServerWithOpts(t *testing.T, mode stateless.Mode, opts ...Option) (*Server, string, func()) {
	t.Helper()
	s := NewServer(core.ServerInfo{Name: "stateless-cap-test", Version: "0.0.1"}, opts...)
	if err := s.Registry().AddTool(
		core.ToolDef{Name: "echo"},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.ToolResult{}, nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(mode))
	ts := httptest.NewServer(handler)
	return s, ts.URL + "/mcp", func() { ts.Close() }
}

func openSubscriptionWithHeader(t *testing.T, ctx context.Context, url, header, value string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"notifications": map[string]bool{"toolsListChanged": true},
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(mcpProtocolVersionHeader, draftVersion)
	if header != "" {
		req.Header.Set(header, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// TestStatelessSubscriptionWire_RejectionShape proves the end-to-end
// rejection: a second subscriptions/listen from the same scope (cap=1)
// returns HTTP 429 with a JSON-RPC -32010 body carrying reason=cap_exceeded.
func TestStatelessSubscriptionWire_RejectionShape(t *testing.T) {
	scopeFn := func(r *http.Request) string { return r.Header.Get("X-Test-Scope") }
	_, url, teardown := newStatelessTestServerWithOpts(t, stateless.ModeDual,
		WithStatelessSubscriptionCap(1),
		WithStatelessSubscriptionScope(scopeFn),
	)
	defer teardown()

	// First subscribe succeeds and stays open.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := openSubscriptionWithHeader(t, ctx, url, "X-Test-Scope", "scope-A")
	if first.StatusCode != 200 {
		t.Fatalf("first subscribe: status %d", first.StatusCode)
	}
	t.Cleanup(func() { first.Body.Close() })

	// Read the ack frame so we know the handler is in its loop (not racy).
	_ = readSSEFrames(t, first, 1, 1*time.Second)

	// Second subscribe from same scope is refused.
	second := openSubscriptionWithHeader(t, ctx, url, "X-Test-Scope", "scope-A")
	defer second.Body.Close()
	if second.StatusCode != 429 {
		raw, _ := io.ReadAll(second.Body)
		t.Fatalf("rejected subscribe: status %d, want 429; body=%s", second.StatusCode, raw)
	}
	if !strings.HasPrefix(second.Header.Get("Content-Type"), "application/json") {
		t.Errorf("rejected subscribe: Content-Type = %q, want application/json", second.Header.Get("Content-Type"))
	}
	raw, err := io.ReadAll(second.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var resp core.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal body: %v\nraw=%s", err, raw)
	}
	if resp.Error == nil {
		t.Fatalf("want error response, got %#v", resp)
	}
	if resp.Error.Code != core.ErrCodeSubscriptionLimitExceeded {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeSubscriptionLimitExceeded)
	}
	if data, ok := resp.Error.Data.(map[string]any); ok {
		if data["reason"] != "cap_exceeded" {
			t.Errorf("error.data.reason = %v, want cap_exceeded", data["reason"])
		}
		if data["scope"] != "scope-A" {
			t.Errorf("error.data.scope = %v, want scope-A", data["scope"])
		}
	} else {
		t.Errorf("error.data shape = %#v", resp.Error.Data)
	}
}

// TestStatelessSubscriptionWire_UnlimitedDefault confirms that without
// any cap option, the wire admits multiple concurrent streams from a
// single scope. Backstops the "no behavioral regression" claim.
func TestStatelessSubscriptionWire_UnlimitedDefault(t *testing.T) {
	_, url, teardown := newStatelessTestServerWithOpts(t, stateless.ModeDual)
	defer teardown()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 4; i++ {
		resp := openSubscriptionWithHeader(t, ctx, url, "", "")
		if resp.StatusCode != 200 {
			t.Fatalf("default cap should be unlimited; subscribe %d: status %d", i+1, resp.StatusCode)
		}
		t.Cleanup(func() { resp.Body.Close() })
	}
}

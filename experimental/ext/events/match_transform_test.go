package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

// Per-subscription Match / Transform on broadcast emit (η-4).
//
// Coverage targets per docs/EVENTS_ETA_PLAN.md η-4 acceptance:
//   - Match returning false filters that subscriber out.
//   - Transform changes the delivered event per subscriber.
//   - nil hooks are no-op (passthrough; baseline).
//   - Panicking hooks recover safely (match=false, transform=identity).
//   - Webhook bodies re-marshal + re-sign per target ONLY when
//     transform actually returned (_, true).
//   - Cross-mode parity: same EventDef, same emit → push / webhook /
//     poll all agree on which subscribers see the event and what
//     shape it arrives in.

type sevPayload struct {
	Severity string `json:"severity"`
	Reporter string `json:"reporter"`
}

// matchOnSeverity returns a MatchFunc that delivers the event only
// when the subscriber's params.severity equals the event's severity
// (or when params.severity is unset → deliver-all). Mirrors the
// canonical spec example at L633-644.
func matchOnSeverity() MatchFunc {
	return func(_ HookContext, event Event, params map[string]any) bool {
		want, ok := params["severity"].(string)
		if !ok || want == "" {
			return true
		}
		var p sevPayload
		if err := json.Unmarshal(event.Data, &p); err != nil {
			return true // unparseable → don't filter (fail open)
		}
		return p.Severity == want
	}
}

// transformRedactReporter blanks the Reporter field when the
// subscriber's params.redact_pii is truthy. Returns (modified, true)
// only when it actually redacted, so the wiring's Q8 short-circuit
// can skip re-marshal on the passthrough path.
func transformRedactReporter() TransformFunc {
	return func(_ HookContext, event Event, params map[string]any) (Event, bool) {
		v, _ := params["redact_pii"].(bool)
		if !v {
			return event, false
		}
		var p sevPayload
		if err := json.Unmarshal(event.Data, &p); err != nil {
			return event, false
		}
		p.Reporter = ""
		raw, err := json.Marshal(p)
		if err != nil {
			return event, false
		}
		out := event
		out.Data = raw
		return out, true
	}
}

// --- Push fanout (yield → Subscribe channel) ---

func TestMatchTransform_Push_MatchFiltersSubscribers(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "match-only push test",
		Delivery:    []string{"push"},
		Match:       matchOnSeverity(),
	}
	src, yield := NewYieldingSource[sevPayload](def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chHigh := src.Subscribe(ctx, SubscribeOpts{Principal: "alice", Params: map[string]any{"severity": "high"}})
	chLow := src.Subscribe(ctx, SubscribeOpts{Principal: "bob", Params: map[string]any{"severity": "low"}})
	chAll := src.Subscribe(ctx, SubscribeOpts{Principal: "carol", Params: nil})

	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "alice@x"}))

	// chHigh + chAll should see it; chLow should not.
	expectEvent := func(label string, ch <-chan SubscriberEvent, want bool) {
		t.Helper()
		select {
		case se := <-ch:
			if !want {
				t.Fatalf("%s: received event but Match should have filtered it: %+v", label, se.Event)
			}
		case <-time.After(100 * time.Millisecond):
			if want {
				t.Fatalf("%s: did not receive event within deadline", label)
			}
		}
	}
	expectEvent("chHigh", chHigh, true)
	expectEvent("chLow", chLow, false)
	expectEvent("chAll", chAll, true)
}

func TestMatchTransform_Push_TransformShapesPerSubscriber(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "transform-only push test",
		Delivery:    []string{"push"},
		Transform:   transformRedactReporter(),
	}
	src, yield := NewYieldingSource[sevPayload](def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chRedact := src.Subscribe(ctx, SubscribeOpts{Params: map[string]any{"redact_pii": true}})
	chRaw := src.Subscribe(ctx, SubscribeOpts{Params: map[string]any{"redact_pii": false}})

	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "alice@x"}))

	pickPayload := func(ch <-chan SubscriberEvent) sevPayload {
		t.Helper()
		select {
		case se := <-ch:
			var p sevPayload
			require.NoError(t, json.Unmarshal(se.Event.Data, &p))
			return p
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("no event")
			return sevPayload{}
		}
	}
	got := pickPayload(chRedact)
	if got.Reporter != "" {
		t.Errorf("redacted subscriber: Reporter = %q, want \"\"", got.Reporter)
	}
	got = pickPayload(chRaw)
	if got.Reporter != "alice@x" {
		t.Errorf("raw subscriber: Reporter = %q, want \"alice@x\"", got.Reporter)
	}
}

func TestMatchTransform_Push_NilHooksAreNoop(t *testing.T) {
	src, yield := NewYieldingSource[sevPayload](EventDef{
		Name:        "alert.fired",
		Description: "nil hooks",
		Delivery:    []string{"push"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := src.Subscribe(ctx, SubscribeOpts{Params: map[string]any{"severity": "low"}})

	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "alice@x"}))

	select {
	case se := <-ch:
		var p sevPayload
		require.NoError(t, json.Unmarshal(se.Event.Data, &p))
		if p.Severity != "high" || p.Reporter != "alice@x" {
			t.Errorf("nil-hooks delivered modified event: %+v", p)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("nil-hooks subscriber did not receive event")
	}
}

func TestMatchTransform_Push_PanicSafe(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "panic-safe push",
		Delivery:    []string{"push"},
		Match: func(HookContext, Event, map[string]any) bool {
			panic("match boom")
		},
		Transform: func(HookContext, Event, map[string]any) (Event, bool) {
			panic("transform boom")
		},
	}
	src, yield := NewYieldingSource[sevPayload](def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := src.Subscribe(ctx, SubscribeOpts{Params: map[string]any{"severity": "high"}})

	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "alice@x"}))

	// Match panic → safeMatch returns false → subscriber does not
	// receive. The fanout itself MUST NOT crash.
	select {
	case <-ch:
		t.Fatalf("panicking match delivered the event; safeMatch should have returned false")
	case <-time.After(100 * time.Millisecond):
	}
}

// --- Webhook fanout (Deliver) ---

func TestMatchTransform_Webhook_MatchAndTransformPerTarget(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "match+transform webhook",
		Delivery:    []string{"webhook"},
		Match:       matchOnSeverity(),
		Transform:   transformRedactReporter(),
	}
	src, yield := NewYieldingSource[sevPayload](def)

	type received struct {
		body []byte
		sub  string
	}
	var bodiesA, bodiesB, bodiesC []received
	var mu sync.Mutex
	mkReceiver := func(sink *[]received) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			mu.Lock()
			*sink = append(*sink, received{body: b, sub: r.Header.Get("X-MCP-Subscription-Id")})
			mu.Unlock()
			w.WriteHeader(200)
		}))
	}
	rA := mkReceiver(&bodiesA)
	rB := mkReceiver(&bodiesB)
	rC := mkReceiver(&bodiesC)
	defer rA.Close()
	defer rB.Close()
	defer rC.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subscribe := func(url string, params map[string]any, secretLetter byte) {
		t.Helper()
		body := map[string]any{
			"name":   "alert.fired",
			"params": params,
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    url,
				"secret": "whsec_" + strings.Repeat(string(secretLetter), 32),
			},
		}
		raw, _ := json.Marshal(body)
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: raw,
		})
		require.NoError(t, err)
		require.Nil(t, resp.Error, "subscribe failed: %+v", resp.Error)
	}
	// A: match high, no redact → receives raw body
	subscribe(rA.URL, map[string]any{"severity": "high"}, 'a')
	// B: match low → filtered out (won't receive)
	subscribe(rB.URL, map[string]any{"severity": "low"}, 'b')
	// C: match high, redact → receives body with empty Reporter
	subscribe(rC.URL, map[string]any{"severity": "high", "redact_pii": true}, 'c')

	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "alice@x"}))

	// Async deliver — wait briefly for all three (or just the
	// expected two) to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		nA, nB, nC := len(bodiesA), len(bodiesB), len(bodiesC)
		mu.Unlock()
		if nA > 0 && nC > 0 && nB == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodiesA) != 1 {
		t.Fatalf("A (match=high) got %d deliveries, want 1", len(bodiesA))
	}
	if len(bodiesB) != 0 {
		t.Fatalf("B (match=low) got %d deliveries, want 0 (Match should filter)", len(bodiesB))
	}
	if len(bodiesC) != 1 {
		t.Fatalf("C (match=high+redact) got %d deliveries, want 1", len(bodiesC))
	}

	parseReporter := func(b []byte) string {
		var env struct {
			Data sevPayload `json:"data"`
		}
		require.NoError(t, json.Unmarshal(b, &env))
		return env.Data.Reporter
	}
	if got := parseReporter(bodiesA[0].body); got != "alice@x" {
		t.Errorf("A: Reporter = %q, want \"alice@x\" (no redact)", got)
	}
	if got := parseReporter(bodiesC[0].body); got != "" {
		t.Errorf("C: Reporter = %q, want \"\" (redacted)", got)
	}

	// Sanity: the redacted body MUST be a different byte string from
	// the raw body — otherwise the HMAC signatures would collide and
	// the per-target re-sign code path is dead.
	if string(bodiesA[0].body) == string(bodiesC[0].body) {
		t.Errorf("redacted target body identical to raw target body — re-marshal-on-modified path didn't run")
	}
}

func TestMatchTransform_Webhook_FiltersByEventName(t *testing.T) {
	// Two sources sharing one registry: a yield from source A must
	// NOT deliver to source B's webhook subscribers. (Pre-η-4 the
	// registry didn't filter by name; η-4's per-target processing
	// adds the filter.)
	srcA, yieldA := NewYieldingSource[sevPayload](EventDef{
		Name:        "src.a",
		Description: "source A",
		Delivery:    []string{"webhook"},
	})
	srcB, _ := NewYieldingSource[sevPayload](EventDef{
		Name:        "src.b",
		Description: "source B",
		Delivery:    []string{"webhook"},
	})

	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	Register(Config{
		Sources:                  []EventSource{srcA, srcB},
		Webhooks:                 wh,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	// Subscribe ONLY to src.b
	body := map[string]any{
		"name": "src.b",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`),
		Method: "events/subscribe", Params: raw,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	// Yield from src.a — should NOT deliver to src.b's webhook.
	require.NoError(t, yieldA(sevPayload{Severity: "high"}))

	time.Sleep(150 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Errorf("src.a yield delivered to src.b's webhook %d times; want 0 (event-name filter)", got)
	}
}

// --- Poll path ---

func TestMatchTransform_Poll_AppliesPerCall(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "poll match+transform",
		Delivery:    []string{"poll"},
		Match:       matchOnSeverity(),
		Transform:   transformRedactReporter(),
	}
	src, yield := NewYieldingSource[sevPayload](def)

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	// Pre-populate two events of differing severity.
	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "h@x"}))
	require.NoError(t, yield(sevPayload{Severity: "low", Reporter: "l@x"}))

	pollAndDecode := func(params map[string]any) []sevPayload {
		t.Helper()
		body := map[string]any{"name": "alert.fired", "params": params, "cursor": "0"}
		raw, _ := json.Marshal(body)
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/poll", Params: raw,
		})
		require.NoError(t, err)
		require.Nil(t, resp.Error, "poll failed: %+v", resp.Error)
		w, ok := resp.Result.(pollResultWire)
		require.True(t, ok, "events/poll Result should be pollResultWire, got %T", resp.Result)
		out := make([]sevPayload, 0, len(w.Events))
		for _, e := range w.Events {
			var p sevPayload
			require.NoError(t, json.Unmarshal(e.Data, &p))
			out = append(out, p)
		}
		return out
	}

	// Caller asking for severity=high gets only the high-severity
	// event, with redact applied.
	got := pollAndDecode(map[string]any{"severity": "high", "redact_pii": true})
	if len(got) != 1 {
		t.Fatalf("severity=high: got %d events, want 1", len(got))
	}
	if got[0].Severity != "high" {
		t.Errorf("severity=high: got severity %q, want \"high\"", got[0].Severity)
	}
	if got[0].Reporter != "" {
		t.Errorf("severity=high+redact: Reporter = %q, want \"\"", got[0].Reporter)
	}

	// Caller without severity filter gets both.
	got = pollAndDecode(nil)
	if len(got) != 2 {
		t.Fatalf("no filter: got %d events, want 2", len(got))
	}
}

// --- Cross-mode parity ---

// TestMatchTransform_CrossModeParity verifies that the same EventDef
// with the same hooks gives consistent filtering and shaping across
// push, webhook, and poll for the same emitted event.
func TestMatchTransform_CrossModeParity(t *testing.T) {
	def := EventDef{
		Name:        "alert.fired",
		Description: "cross-mode parity",
		Delivery:    []string{"poll", "push", "webhook"},
		Match:       matchOnSeverity(),
		Transform:   transformRedactReporter(),
	}
	src, yield := NewYieldingSource[sevPayload](def)

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "alice",
		StreamHeartbeatInterval:  500 * time.Millisecond,
	})
	finishInitHandshake(t, srv)

	matchParams := map[string]any{"severity": "high", "redact_pii": true}

	// Push: subscribe to source directly so we don't have to drive
	// the full events/stream flow.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushCh := src.Subscribe(ctx, SubscribeOpts{Principal: "alice", Params: matchParams})

	// Webhook
	var whBody []byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		whBody = buf
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	subBody, _ := json.Marshal(map[string]any{
		"name":   "alert.fired",
		"params": matchParams,
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`),
		Method: "events/subscribe", Params: subBody,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	// Yield matches.
	require.NoError(t, yield(sevPayload{Severity: "high", Reporter: "h@x"}))

	// Push delivery
	var pushPayload sevPayload
	select {
	case se := <-pushCh:
		require.NoError(t, json.Unmarshal(se.Event.Data, &pushPayload))
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("push subscriber did not receive event")
	}

	// Webhook delivery — async; wait
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && len(whBody) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, whBody, "webhook receiver got no body")
	var whEnv struct {
		Data sevPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(whBody, &whEnv))

	// Poll delivery
	pollBody, _ := json.Marshal(map[string]any{
		"name": "alert.fired", "params": matchParams, "cursor": "0",
	})
	resp, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`),
		Method: "events/poll", Params: pollBody,
	})
	require.NoError(t, err)
	w, ok := resp.Result.(pollResultWire)
	require.True(t, ok, "events/poll Result should be pollResultWire, got %T", resp.Result)
	require.Len(t, w.Events, 1)
	var pollPayload sevPayload
	require.NoError(t, json.Unmarshal(w.Events[0].Data, &pollPayload))

	// All three modes saw the same redacted shape.
	if pushPayload.Reporter != "" || whEnv.Data.Reporter != "" || pollPayload.Reporter != "" {
		t.Errorf("cross-mode parity broken: push=%q webhook=%q poll=%q (want all empty)",
			pushPayload.Reporter, whEnv.Data.Reporter, pollPayload.Reporter)
	}
	if pushPayload.Severity != "high" || whEnv.Data.Severity != "high" || pollPayload.Severity != "high" {
		t.Errorf("cross-mode parity broken: severity push=%q webhook=%q poll=%q",
			pushPayload.Severity, whEnv.Data.Severity, pollPayload.Severity)
	}
}

package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// testHC returns an empty HookContext for tests that don't care about
// its fields (panic recovery, nil-vs-not). Wiring tests that DO care
// (η-3 / η-4) construct via newHookContext directly.
func testHC() HookContext {
	return newHookContext(nil, "", "", deliveryModeUnset)
}

func TestHookContext_Defaults(t *testing.T) {
	// Empty constructor — covers the wiring sub-PRs' fallback path
	// (no auth claims, no sub id) plus the nil-receiver guards on
	// the impl methods (which keep tests/zero values from panicking).
	zero := newHookContext(nil, "", "", deliveryModeUnset)
	if zero.Context() != context.Background() {
		t.Errorf("HookContext.Context() should fall back to context.Background() when ctx is nil, got %v", zero.Context())
	}
	if zero.Principal() != "" {
		t.Errorf("empty HookContext.Principal() = %q, want empty", zero.Principal())
	}
	if zero.SubscriptionID() != "" {
		t.Errorf("empty HookContext.SubscriptionID() = %q, want empty", zero.SubscriptionID())
	}
	if zero.Mode() != deliveryModeUnset {
		t.Errorf("empty HookContext.Mode() = %v, want deliveryModeUnset", zero.Mode())
	}
}

func TestHookContext_NilReceiverSafe(t *testing.T) {
	// A nil *hookContext satisfies HookContext (Go interface trap)
	// — the impl methods must guard against it so authors that
	// somehow obtained a typed-nil don't crash.
	var nilImpl *hookContext
	var hc HookContext = nilImpl
	if hc.Context() != context.Background() {
		t.Errorf("nil-receiver Context() did not fall back to Background()")
	}
	if hc.Principal() != "" {
		t.Errorf("nil-receiver Principal() = %q, want empty", hc.Principal())
	}
	if hc.SubscriptionID() != "" {
		t.Errorf("nil-receiver SubscriptionID() = %q, want empty", hc.SubscriptionID())
	}
	if hc.Mode() != deliveryModeUnset {
		t.Errorf("nil-receiver Mode() = %v, want deliveryModeUnset", hc.Mode())
	}
}

func TestHookContext_Populated(t *testing.T) {
	type ctxKey string
	parent := context.WithValue(context.Background(), ctxKey("k"), "v")
	hc := newHookContext(parent, "alice", "sub_abc", DeliveryModeWebhook)
	if hc.Principal() != "alice" {
		t.Errorf("Principal() = %q, want %q", hc.Principal(), "alice")
	}
	if hc.SubscriptionID() != "sub_abc" {
		t.Errorf("SubscriptionID() = %q, want %q", hc.SubscriptionID(), "sub_abc")
	}
	if hc.Mode() != DeliveryModeWebhook {
		t.Errorf("Mode() = %v, want DeliveryModeWebhook", hc.Mode())
	}
	if got := hc.Context().Value(ctxKey("k")); got != "v" {
		t.Errorf("Context().Value(k) = %v, want \"v\" (context not preserved)", got)
	}
}

func TestDeliveryMode_String(t *testing.T) {
	cases := []struct {
		mode DeliveryMode
		want string
	}{
		{DeliveryModePoll, "poll"},
		{DeliveryModePush, "push"},
		{DeliveryModeWebhook, "webhook"},
		{deliveryModeUnset, "unset"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("DeliveryMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestSafeMatch_NilIsDeliverAll(t *testing.T) {
	if !safeMatch(nil, testHC(), Event{}, nil) {
		t.Errorf("safeMatch(nil) = false, want true (nil match = deliver to all)")
	}
}

func TestSafeMatch_PassthroughResults(t *testing.T) {
	yes := func(HookContext, Event, map[string]any) bool { return true }
	no := func(HookContext, Event, map[string]any) bool { return false }
	if !safeMatch(yes, testHC(), Event{}, nil) {
		t.Errorf("safeMatch(yes) returned false")
	}
	if safeMatch(no, testHC(), Event{}, nil) {
		t.Errorf("safeMatch(no) returned true")
	}
}

func TestSafeMatch_PanicReturnsFalse(t *testing.T) {
	panicky := func(HookContext, Event, map[string]any) bool { panic("oops") }
	if safeMatch(panicky, testHC(), Event{Name: "panic-evt"}, nil) {
		t.Errorf("safeMatch(panicky) returned true; want false on panic")
	}
}

func TestSafeTransform_NilIsPassthrough(t *testing.T) {
	in := Event{Name: "x", EventID: "evt_1"}
	out, modified := safeTransform(nil, testHC(), in, nil)
	if modified {
		t.Errorf("safeTransform(nil) modified=true; want false")
	}
	if out.EventID != in.EventID || out.Name != in.Name {
		t.Errorf("safeTransform(nil) returned %+v; want passthrough of %+v", out, in)
	}
}

func TestSafeTransform_PassthroughResults(t *testing.T) {
	in := Event{Name: "x", Data: json.RawMessage(`{"a":1}`)}
	redact := func(_ HookContext, e Event, _ map[string]any) (Event, bool) {
		e.Data = json.RawMessage(`{"a":null}`)
		return e, true
	}
	out, modified := safeTransform(redact, testHC(), in, nil)
	if !modified {
		t.Errorf("safeTransform(redact) modified=false; want true")
	}
	if string(out.Data) != `{"a":null}` {
		t.Errorf("safeTransform(redact) data = %s, want {\"a\":null}", out.Data)
	}
	// Original event must not have been mutated under us.
	if string(in.Data) != `{"a":1}` {
		t.Errorf("safeTransform mutated input event: %s", in.Data)
	}
}

func TestSafeTransform_PanicReturnsOriginal(t *testing.T) {
	in := Event{Name: "x", EventID: "evt_1", Data: json.RawMessage(`{"keep":true}`)}
	panicky := func(HookContext, Event, map[string]any) (Event, bool) { panic("nope") }
	out, modified := safeTransform(panicky, testHC(), in, nil)
	if modified {
		t.Errorf("safeTransform(panic) modified=true; want false")
	}
	if out.EventID != in.EventID || string(out.Data) != string(in.Data) {
		t.Errorf("safeTransform(panic) = %+v; want original %+v", out, in)
	}
}

func TestSafeOnSubscribe_NilIsNoop(t *testing.T) {
	if err := safeOnSubscribe(nil, testHC(), nil); err != nil {
		t.Errorf("safeOnSubscribe(nil) = %v; want nil", err)
	}
}

func TestSafeOnSubscribe_ErrorIsReturned(t *testing.T) {
	want := errors.New("upstream quota")
	fn := func(HookContext, map[string]any) error { return want }
	if got := safeOnSubscribe(fn, testHC(), nil); !errors.Is(got, want) {
		t.Errorf("safeOnSubscribe returned %v; want %v", got, want)
	}
}

func TestSafeOnSubscribe_PanicSwallowed(t *testing.T) {
	fn := func(HookContext, map[string]any) error { panic("boom") }
	if err := safeOnSubscribe(fn, testHC(), nil); err != nil {
		t.Errorf("safeOnSubscribe(panic) = %v; want nil (panic swallowed)", err)
	}
}

func TestSafeOnUnsubscribe_NilIsNoop(t *testing.T) {
	// Just must not panic.
	safeOnUnsubscribe(nil, testHC(), nil)
}

func TestSafeOnUnsubscribe_PanicSwallowed(t *testing.T) {
	fn := func(HookContext, map[string]any) { panic("teardown failed") }
	// Must not propagate.
	safeOnUnsubscribe(fn, testHC(), nil)
}

func TestSafeOnUnsubscribe_FunctionRuns(t *testing.T) {
	called := false
	fn := func(HookContext, map[string]any) { called = true }
	safeOnUnsubscribe(fn, testHC(), nil)
	if !called {
		t.Errorf("safeOnUnsubscribe did not invoke the function")
	}
}

// Hook fields on EventDef must be tagged json:"-" so they don't leak
// onto the wire when events/list serializes the EventDef. Verifies the
// struct tag rather than just round-tripping JSON because the function
// fields would fail to marshal at all (functions have no JSON form);
// the test exists to lock the tag in place against future edits.
func TestEventDef_HooksOmittedFromJSON(t *testing.T) {
	def := EventDef{
		Name:        "test.event",
		Description: "ensures hooks don't leak",
		Delivery:    []string{"poll"},
		Match: func(HookContext, Event, map[string]any) bool {
			return true
		},
		Transform: func(_ HookContext, e Event, _ map[string]any) (Event, bool) {
			return e, false
		},
		OnSubscribe:   func(HookContext, map[string]any) error { return nil },
		OnUnsubscribe: func(HookContext, map[string]any) {},
	}
	out, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("json.Marshal(EventDef) returned error: %v", err)
	}
	s := string(out)
	for _, banned := range []string{`"Match"`, `"Transform"`, `"OnSubscribe"`, `"OnUnsubscribe"`,
		`"match"`, `"transform"`, `"onSubscribe"`, `"onUnsubscribe"`} {
		if strings.Contains(s, banned) {
			t.Errorf("EventDef JSON contains hook field %s; output: %s", banned, s)
		}
	}
	if !strings.Contains(s, `"name":"test.event"`) {
		t.Errorf("EventDef JSON missing name; output: %s", s)
	}
}

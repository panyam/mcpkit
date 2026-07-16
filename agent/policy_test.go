package agent

import (
	"encoding/json"

	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

func evt(server, name, payload string) IncomingEvent {
	return IncomingEvent{Server: server, Name: name, Data: core.NewRawJSON(json.RawMessage(payload))}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
}

func TestInjectionMergeWindowCoalescesBurst(t *testing.T) {
	clock := newClock()
	p := NewInjectionPolicy(InjectionConfig{
		Hints: map[string]ContextHint{
			"cart.changed": {Aggregate: &AggregateHint{WindowMs: 2000, Strategy: WindowMerge},
				Template: "cart has {{count}} items"},
		},
		now: clock.now,
	})

	p.Ingest(evt("grocery", "cart.changed", `{"count":1}`))
	p.Ingest(evt("grocery", "cart.changed", `{"count":2}`))
	p.Ingest(evt("grocery", "cart.changed", `{"count":3}`))

	if got := p.Drain(); len(got) != 0 {
		t.Fatalf("window must hold the burst until expiry, got %v", got)
	}
	clock.advance(3 * time.Second)
	got := p.Drain()
	if len(got) != 1 || got[0].Text != "cart has 3 items" {
		t.Fatalf("burst must coalesce to one entry with merged fields: %+v", got)
	}
	if len(p.Drain()) != 0 {
		t.Fatal("turn retention: drained entries must not repeat")
	}
}

func TestInjectionDebounceRestartsWindow(t *testing.T) {
	clock := newClock()
	p := NewInjectionPolicy(InjectionConfig{
		Hints: map[string]ContextHint{
			"typing": {Aggregate: &AggregateHint{WindowMs: 1000, Strategy: WindowDebounce}},
		},
		now: clock.now,
	})
	p.Ingest(evt("s", "typing", `{"n":1}`))
	clock.advance(800 * time.Millisecond)
	p.Ingest(evt("s", "typing", `{"n":2}`))
	clock.advance(800 * time.Millisecond)
	if got := p.Drain(); len(got) != 0 {
		t.Fatalf("debounce must restart on each event, got %+v", got)
	}
	clock.advance(300 * time.Millisecond)
	if got := p.Drain(); len(got) != 1 || !strings.Contains(got[0].Text, `"n":2`) {
		t.Fatalf("quiet gap must release the last event: %+v", got)
	}
}

func TestInjectionPriorityOrderAndBudget(t *testing.T) {
	clock := newClock()
	p := NewInjectionPolicy(InjectionConfig{
		Hints: map[string]ContextHint{
			"low.ev":  {Priority: "low"},
			"crit.ev": {Priority: "critical"},
			"high.ev": {Priority: "high"},
		},
		MaxPerDrain: 2,
		now:         clock.now,
	})
	p.Ingest(evt("s", "low.ev", `{}`))
	p.Ingest(evt("s", "crit.ev", `{}`))
	p.Ingest(evt("s", "high.ev", `{}`))

	got := p.Drain()
	if len(got) != 2 || got[0].Event.Name != "crit.ev" || got[1].Event.Name != "high.ev" {
		t.Fatalf("priority order under budget broken: %+v", got)
	}
	rest := p.Drain()
	if len(rest) != 1 || rest[0].Event.Name != "low.ev" {
		t.Fatalf("over-budget entries must carry to the next drain: %+v", rest)
	}
}

func TestInjectionSensitivityGate(t *testing.T) {
	clock := newClock()
	consented := false
	p := NewInjectionPolicy(InjectionConfig{
		Hints: map[string]ContextHint{
			"secret.ev": {Sensitivity: "restricted"},
			"pers.ev":   {Sensitivity: "personal"},
		},
		now: clock.now,
	})
	p.Ingest(evt("s", "secret.ev", `{}`))
	p.Ingest(evt("s", "pers.ev", `{}`))
	got := p.Drain()
	if len(got) != 1 || got[0].Event.Name != "pers.ev" {
		t.Fatalf("default gate: personal passes, restricted drops: %+v", got)
	}
	if p.Dropped() != 1 {
		t.Fatalf("dropped count = %d", p.Dropped())
	}

	p2 := NewInjectionPolicy(InjectionConfig{
		Hints:   map[string]ContextHint{"secret.ev": {Sensitivity: "restricted"}},
		Consent: func(h ContextHint, ev IncomingEvent) bool { consented = true; return true },
		now:     clock.now,
	})
	p2.Ingest(evt("s", "secret.ev", `{}`))
	if got := p2.Drain(); len(got) != 1 || !consented {
		t.Fatalf("consent gate must decide restricted: %+v consented=%v", got, consented)
	}
}

func TestInjectionSessionRetentionReinjectsLatest(t *testing.T) {
	clock := newClock()
	p := NewInjectionPolicy(InjectionConfig{
		Hints: map[string]ContextHint{"loc": {Retention: "session", Template: "at {{city}}"}},
		now:   clock.now,
	})
	p.Ingest(evt("s", "loc", `{"city":"Paris"}`))
	if got := p.Drain(); len(got) != 1 || got[0].Text != "at Paris" {
		t.Fatalf("first drain: %+v", got)
	}
	if got := p.Drain(); len(got) != 1 || got[0].Text != "at Paris" {
		t.Fatalf("session retention must re-inject: %+v", got)
	}
	p.Ingest(evt("s", "loc", `{"city":"Rome"}`))
	if got := p.Drain(); len(got) != 1 || got[0].Text != "at Rome" {
		t.Fatalf("newer occurrence must supersede: %+v", got)
	}
}

func TestInjectionDeveloperStagesAndHintFromMeta(t *testing.T) {
	clock := newClock()
	type cart struct {
		Count int    `json:"count"`
		User  string `json:"user"`
	}
	p := NewInjectionPolicy(InjectionConfig{
		Filters: []func(IncomingEvent) bool{
			TypedFilter(func(c cart) bool { return c.Count > 0 }),
		},
		Transforms: []func(IncomingEvent) (IncomingEvent, bool){
			TypedTransform(func(c cart) (cart, bool) { c.User = "[redacted]"; return c, true }),
		},
		now: clock.now,
	})
	p.Ingest(evt("s", "cart.changed", `{"count":0,"user":"alice"}`))
	p.Ingest(evt("s", "cart.changed", `{"count":2,"user":"alice"}`))
	got := p.Drain()
	if len(got) != 1 {
		t.Fatalf("typed filter must drop count=0: %+v", got)
	}
	if !strings.Contains(got[0].Text, "[redacted]") || strings.Contains(got[0].Text, "alice") {
		t.Fatalf("typed transform must redact: %s", got[0].Text)
	}

	hint, ok := HintFromMeta(map[string]any{
		MetaKeyContextHint: map[string]any{
			"priority":  "high",
			"template":  "cart: {{count}}",
			"aggregate": map[string]any{"windowMs": float64(1500), "strategy": "merge"},
		},
	})
	if !ok || hint.Priority != "high" || hint.Aggregate == nil || hint.Aggregate.WindowMs != 1500 || hint.Aggregate.Strategy != WindowMerge {
		t.Fatalf("HintFromMeta = %+v ok=%v", hint, ok)
	}
	p.SetHint("cart.changed", hint)
	p.Ingest(evt("s", "cart.changed", `{"count":5,"user":"x"}`))
	if got := p.Drain(); len(got) != 0 {
		t.Fatalf("meta hint's window must now buffer: %+v", got)
	}
	clock.advance(2 * time.Second)
	if got := p.Drain(); len(got) != 1 || got[0].Text != "cart: 5" {
		t.Fatalf("meta hint template must render: %+v", got)
	}
}

func TestTriggerSlotMachine(t *testing.T) {
	clock := newClock()
	tp := NewTriggerPolicy(TriggerPolicyConfig{
		Bindings: []TriggerBinding{{
			Event:        "cart.changed",
			Filter:       TypedFilter(func(c struct{ Count int }) bool { return c.Count >= 2 }),
			Instructions: "Offer a recipe.",
			Label:        "recipe-pitch",
			Cooldown:     time.Minute,
		}},
		now: clock.now,
	})

	if tp.OnEvent(evt("g", "cart.changed", `{"Count":1}`)) != nil {
		t.Fatal("filter must suppress below threshold")
	}
	f := tp.OnEvent(evt("g", "cart.changed", `{"Count":2}`))
	if f == nil || f.Binding.Label != "recipe-pitch" {
		t.Fatalf("first qualifying event must fire: %+v", f)
	}
	for i := 0; i < 3; i++ {
		if tp.OnEvent(evt("g", "cart.changed", `{"Count":5}`)) != nil {
			t.Fatal("spent slot must stay quiet")
		}
	}

	tp.NotifyEngagement()
	if tp.OnEvent(evt("g", "cart.changed", `{"Count":5}`)) != nil {
		t.Fatal("engagement alone must not re-arm before cooldown")
	}
	clock.advance(2 * time.Minute)
	if tp.OnEvent(evt("g", "cart.changed", `{"Count":5}`)) == nil {
		t.Fatal("engagement plus cooldown must re-arm")
	}

	clock2 := newClock()
	tp2 := NewTriggerPolicy(TriggerPolicyConfig{
		Bindings: []TriggerBinding{{Event: "e", Label: "l", Cooldown: time.Millisecond}},
		Budget:   1,
		now:      clock2.now,
	})
	if tp2.OnEvent(evt("s", "e", `{}`)) == nil {
		t.Fatal("first firing within budget")
	}
	tp2.NotifyEngagement()
	clock2.advance(time.Second)
	if tp2.OnEvent(evt("s", "e", `{}`)) != nil {
		t.Fatal("budget must cap firings even when re-armed")
	}
	if tp2.Firings() != 1 {
		t.Fatalf("firings = %d", tp2.Firings())
	}
}

func TestTriggerConsentGate(t *testing.T) {
	declined := 0
	tp := NewTriggerPolicy(TriggerPolicyConfig{
		Bindings: []TriggerBinding{{Event: "e", Label: "l"}},
		Consent:  func(TriggerBinding, IncomingEvent) bool { declined++; return false },
	})
	if tp.OnEvent(evt("s", "e", `{}`)) != nil {
		t.Fatal("declined consent must suppress")
	}
	if declined != 1 || tp.Firings() != 0 {
		t.Fatalf("declined=%d firings=%d", declined, tp.Firings())
	}
}

func TestGenericWindowIsPromotable(t *testing.T) {
	// The stages are generic machinery: prove they work on a non-event
	// type (the promotability contract from the design discussion).
	clock := newClock()
	w := Window[int](time.Second, WindowMerge, func(int) string { return "k" }, func(a, b int) int { return a + b })
	w.Push(clock.t, 1)
	w.Push(clock.t, 2)
	w.Push(clock.t, 3)
	if got := w.Flush(clock.t.Add(2 * time.Second)); len(got) != 1 || got[0] != 6 {
		t.Fatalf("generic merge window = %v", got)
	}

	pipe := NewPipeline[int](
		Filter(func(n int) bool { return n%2 == 0 }),
		Transform(func(n int) (int, bool) { return n * 10, true }),
	)
	if got := pipe.Push(clock.t, 4); len(got) != 1 || got[0] != 40 {
		t.Fatalf("generic pipeline = %v", got)
	}
	if got := pipe.Push(clock.t, 3); got != nil {
		t.Fatalf("filter must drop odd: %v", got)
	}
}

func TestSystemRoleReachesProviderWire(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://unused", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	body := p.buildBody(ProviderRequest{
		Messages: []Message{
			{Role: RoleSystem, Text: "event cart.changed from grocery: 3 items"},
			{Role: RoleUser, Text: "what changed?"},
		},
	}, false)
	raw, _ := json.Marshal(body["messages"])
	want := `[{"content":"event cart.changed from grocery: 3 items","role":"system"},{"content":"what changed?","role":"user"}]`
	if string(raw) != want {
		t.Fatalf("system role wire shape:\n got %s\nwant %s", raw, want)
	}
}

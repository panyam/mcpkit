package host

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"net/http/httptest"
)

type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

type cartData struct {
	Count int `json:"count"`
}

func startEventsServer(t *testing.T) (*httptest.Server, func(context.Context, cartData) error) {
	t.Helper()
	srv := testutil.NewTestServer()
	src, yield := events.NewYieldingSource[cartData](events.EventDef{
		Name:        "cart.changed",
		Description: "cart contents changed",
	})
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Server:                   srv,
		UnsafeAnonymousPrincipal: "agentchat-test",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts, yield
}

func waitFor(t *testing.T, out *syncWriter, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in transcript:\n%s", want, out.String())
}

func TestAppEventsInjectionAndTriggerEndToEnd(t *testing.T) {
	ts, yield := startEventsServer(t)

	cfg := testConfig(ts.URL)
	cfg.Instructions = "base"
	cfg.Servers[0].Events = []EventConfig{{
		Name: "cart.changed",
		Hint: &agent.ContextHint{
			Aggregate: &agent.AggregateHint{WindowMs: 300, Strategy: agent.WindowMerge},
			Template:  "the cart now holds {{count}} items",
		},
	}}
	cfg.Triggers = []TriggerConfig{{
		Event:        "cart.changed",
		Filter:       map[string]string{"count": "2"},
		Instructions: "The user's cart changed. Offer exactly one recipe suggestion.",
		Label:        "recipe-pitch",
		CooldownSec:  3600,
	}}

	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "How about pasta tonight?"},
		agent.StubTurn{Text: "Your cart has 3 items."},
	)
	out := &syncWriter{}
	app, err := NewApp(cfg, out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// A qualifying event fires the trigger exactly once.
	if err := yield(context.Background(), cartData{Count: 2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "· trigger: recipe-pitch")
	waitFor(t, out, "How about pasta tonight?")

	proactive := stub.Requests()[0]
	var sawInstr bool
	for _, m := range proactive.Messages {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, "Offer exactly one recipe suggestion") {
			sawInstr = true
		}
	}
	if !sawInstr {
		t.Fatalf("proactive turn must carry the binding instructions as a system message: %+v", proactive.Messages)
	}

	// More qualifying events: the spent slot stays quiet (anti-nag).
	yield(context.Background(), cartData{Count: 2})
	yield(context.Background(), cartData{Count: 2})
	time.Sleep(400 * time.Millisecond)
	if n := strings.Count(out.String(), "· trigger: recipe-pitch"); n != 1 {
		t.Fatalf("spent slot must not re-fire, got %d pitches:\n%s", n, out.String())
	}

	// The burst coalesced in the merge window and injects on the next
	// user turn as one system message.
	yield(context.Background(), cartData{Count: 3})
	time.Sleep(400 * time.Millisecond)
	if err := app.RunTurn(context.Background(), "what's in my cart?"); err != nil {
		t.Fatal(err)
	}
	userReq := stub.Requests()[1]
	var injected []string
	for _, m := range userReq.Messages {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, "cart now holds") {
			injected = append(injected, m.Text)
		}
	}
	if len(injected) != 1 || !strings.Contains(injected[0], "3 items") {
		t.Fatalf("burst must inject as ONE coalesced system message with the latest count: %v\nall: %+v", injected, userReq.Messages)
	}
}

func TestAppEventsWithoutHintsDegradesToRawInjection(t *testing.T) {
	ts, yield := startEventsServer(t)
	cfg := testConfig(ts.URL)
	cfg.Servers[0].Events = []EventConfig{{Name: "cart.changed"}}

	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	out := &syncWriter{}
	app, err := NewApp(cfg, out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	yield(context.Background(), cartData{Count: 7})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.turnMu.Lock()
		app.drainInjectionLocked()
		n := len(app.history)
		app.turnMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	app.turnMu.Lock()
	defer app.turnMu.Unlock()
	if len(app.history) == 0 || app.history[0].Role != agent.RoleSystem ||
		!strings.Contains(app.history[0].Text, `event cart.changed from test`) {
		t.Fatalf("hintless event must inject raw, immediately: %+v", app.history)
	}
}

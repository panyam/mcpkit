package host

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

type userData struct {
	Email string `json:"email"`
}

// controlPlaneServer: an events source (user.created) + a plain tool
// (send_email) the model calls from the trigger's proactive turn.
func controlPlaneServer(t *testing.T) (*httptest.Server, func(context.Context, userData) error, *[]string) {
	t.Helper()
	srv := testutil.NewTestServer()
	src, yield := events.NewYieldingSource[userData](events.EventDef{Name: "user.created", Description: "a user was created"})
	events.Register(events.Config{Sources: []events.EventSource{src}, Server: srv, UnsafeAnonymousPrincipal: "cp-test"})

	var sent []string
	srv.Register(core.TextTool[struct {
		To string `json:"to"`
	}]("send_email", "sends an email",
		func(ctx core.ToolContext, in struct {
			To string `json:"to"`
		}) (string, error) {
			sent = append(sent, in.To)
			return "sent to " + in.To, nil
		}))

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts, yield, &sent
}

// TestControlPlaneSubscribeTriggerAct is the flagship acceptance: the model,
// through conversation, subscribes to an event and installs a standing
// trigger; when the event later fires, the trigger's proactive turn runs and
// the model calls send_email — the Msg-1/2/3 transcript, deterministic via
// StubProvider.
func TestControlPlaneSubscribeTriggerAct(t *testing.T) {
	ts, yield, sent := controlPlaneServer(t)
	cfg := testConfig(ts.URL)
	cfg.MetaTools = true

	stub := agent.NewStubProvider(
		// Turn 1: user says "email me when users are created". Model
		// subscribes and installs the trigger, then confirms.
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "s1", Name: "subscribe_events",
			Args: core.NewRawJSON(json.RawMessage(`{"server":"test","name":"user.created"}`))}}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "t1", Name: "create_trigger",
			Args: core.NewRawJSON(json.RawMessage(`{"event":"user.created","instructions":"send a welcome email to the new user","label":"welcome"}`))}}},
		agent.StubTurn{Text: "Done — I'll email new users."},
		// Proactive turn when user.created fires: model calls send_email.
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "e1", Name: "send_email",
			Args: core.NewRawJSON(json.RawMessage(`{"to":"ada@example.com"}`))}}},
		agent.StubTurn{Text: "Sent a welcome email to ada@example.com."},
	)

	out := &syncWriter{}
	app, err := NewApp(cfg, out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "email me whenever a user is created"); err != nil {
		t.Fatal(err)
	}
	// Both meta-tools fired in the setup turn.
	setup := stub.Requests()
	if !containsToolMsg(setup[1].Messages, "subscribed: test:user.created") {
		t.Fatalf("subscribe_events must have run: %+v", setup[1].Messages)
	}
	if !containsToolMsg(setup[2].Messages, "trigger installed") {
		t.Fatalf("create_trigger must have run: %+v", setup[2].Messages)
	}

	// The event fires; the standing trigger runs a proactive turn that calls
	// send_email.
	if err := yield(context.Background(), userData{Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "· trigger: welcome")
	waitFor(t, out, "Sent a welcome email to ada@example.com.")
	if len(*sent) != 1 || (*sent)[0] != "ada@example.com" {
		t.Fatalf("trigger's proactive turn must have called send_email: %v", *sent)
	}
}

func TestMetaToolsListAndCancelSubscription(t *testing.T) {
	ts, _, _ := controlPlaneServer(t)
	cfg := testConfig(ts.URL)
	cfg.MetaTools = true
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	id, err := app.openSubscription(context.Background(), "test", "user.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(app.listSubscriptions()) != 1 {
		t.Fatal("subscription must register")
	}
	if _, err := app.openSubscription(context.Background(), "test", "user.created"); err != nil {
		t.Fatal(err)
	}
	if len(app.listSubscriptions()) != 1 {
		t.Fatal("re-subscribe must be idempotent")
	}
	if !app.closeSubscription(id) {
		t.Fatal("close must succeed")
	}
	if len(app.listSubscriptions()) != 0 {
		t.Fatal("close must deregister")
	}
	if app.closeSubscription(id) {
		t.Fatal("closing an absent subscription must be false")
	}
}

func containsToolMsg(msgs []agent.Message, want string) bool {
	for _, m := range msgs {
		if m.Role == agent.RoleTool && strings.Contains(m.Text, want) {
			return true
		}
	}
	return false
}

var _ = time.Second

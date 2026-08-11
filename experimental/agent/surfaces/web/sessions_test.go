package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
	agentwebv1 "github.com/panyam/mcpkit/experimental/agent/surfaces/web/gen/go/mcpkit/agentweb/v1"
	"github.com/panyam/mcpkit/experimental/agent/surfaces/web/gen/go/mcpkit/agentweb/v1/agentwebv1connect"
	"github.com/panyam/mcpkit/core"
)

// stubApp builds an App over a fresh StubProvider with the given scripted turns,
// discarding terminal output. Each call is an independent session (its own event
// log and provider), which is what the SessionManager hands out per session.
func stubApp(t *testing.T, turns ...agent.StubTurn) *host.App {
	t.Helper()
	stub := agent.NewStubProvider(turns...)
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""), host.WithProvider(stub))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

// startWatchSession drives a Watch stream for a specific session in the
// background and records frames (the session-aware sibling of startWatch).
func startWatchSession(t *testing.T, ctx context.Context, client agentwebv1connect.HostServiceClient, sessionID string) *watcher {
	t.Helper()
	stream, err := client.Watch(ctx, connect.NewRequest(&agentwebv1.WatchRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("Watch(%q): %v", sessionID, err)
	}
	w := &watcher{}
	go func() {
		for stream.Receive() {
			f := stream.Msg()
			w.mu.Lock()
			w.frames = append(w.frames, f)
			w.mu.Unlock()
		}
	}()
	return w
}

// TestBridge_SessionsAreIsolated is the multi-session acceptance: two sessions
// on one server have independent event streams. A Submit on session A appears on
// A's Watch but never on B's, and B runs its own turn independently.
func TestBridge_SessionsAreIsolated(t *testing.T) {
	mgr := NewSessionManager(func(context.Context) (*host.App, error) {
		return stubApp(t, agent.StubTurn{Text: "reply", FinishReason: "stop"}), nil
	})
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idA := mustCreate(t, ctx, client)
	idB := mustCreate(t, ctx, client)
	if idA == idB {
		t.Fatalf("CreateSession minted colliding ids: %q", idA)
	}

	wa := startWatchSession(t, ctx, client, idA)
	wb := startWatchSession(t, ctx, client, idB)

	// A turn on session A.
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "hi A", SessionId: idA})); err != nil {
		t.Fatalf("Submit A: %v", err)
	}
	wa.waitForKind(t, string(host.HostTurnDone))

	// Session B's stream must NOT carry A's turn.
	time.Sleep(150 * time.Millisecond) // let any cross-talk arrive if it were going to
	if contains(wb.kinds(), string(host.HostTurnDone)) {
		t.Fatalf("session B saw session A's turn-done; B frames=%v", wb.kinds())
	}

	// B runs its own turn independently.
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "hi B", SessionId: idB})); err != nil {
		t.Fatalf("Submit B: %v", err)
	}
	wb.waitForKind(t, string(host.HostTurnDone))
}

// TestBridge_RespondToAskTargetsSession is the multi-session ask acceptance: an
// ask raised on session A is answerable only through session A. B's stream never
// sees the ask, a RespondToAsk aimed at B is refused, and the A-targeted respond
// wins the turn.
func TestBridge_RespondToAskTargetsSession(t *testing.T) {
	askApp := askGatedApp(t)
	idleApp := stubApp(t, agent.StubTurn{Text: "idle", FinishReason: "stop"})

	// Hand the ask-gated app out first, then the idle one, so their ids are known.
	apps := []*host.App{askApp, idleApp}
	var mu sync.Mutex
	var i int
	mgr := NewSessionManager(func(context.Context) (*host.App, error) {
		mu.Lock()
		defer mu.Unlock()
		a := apps[i]
		i++
		return a, nil
	})
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idAsk := mustCreate(t, ctx, client)
	idIdle := mustCreate(t, ctx, client)

	wAsk := startWatchSession(t, ctx, client, idAsk)
	wIdle := startWatchSession(t, ctx, client, idIdle)

	submitErr := make(chan error, 1)
	go func() {
		_, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "please act", SessionId: idAsk}))
		submitErr <- err
	}()

	frame := wAsk.waitForKind(t, string(host.HostElicitRequest))
	var reqEv host.HostEvent
	if err := json.Unmarshal(frame.GetPayload(), &reqEv); err != nil {
		t.Fatalf("decode elicit-request frame: %v", err)
	}
	if reqEv.AskID == 0 {
		t.Fatalf("elicit-request frame carried no AskID: %s", frame.GetPayload())
	}

	// The idle session's stream never saw the ask.
	if contains(wIdle.kinds(), string(host.HostElicitRequest)) {
		t.Fatalf("idle session saw the ask; idle frames=%v", wIdle.kinds())
	}

	// A RespondToAsk aimed at the wrong session is refused (the offset is not a
	// pending ask in the idle session's log).
	result, _ := json.Marshal(core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}})
	_, err := client.RespondToAsk(ctx, connect.NewRequest(&agentwebv1.RespondToAskRequest{
		AskId: reqEv.AskID, Result: result, By: "web", SessionId: idIdle,
	}))
	if err == nil {
		t.Fatalf("RespondToAsk aimed at the idle session should have failed")
	}
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("wrong-session RespondToAsk code = %v, want FailedPrecondition", got)
	}

	// The correctly targeted respond wins the ask.
	if _, err := client.RespondToAsk(ctx, connect.NewRequest(&agentwebv1.RespondToAskRequest{
		AskId: reqEv.AskID, Result: result, By: "web", SessionId: idAsk,
	})); err != nil {
		t.Fatalf("RespondToAsk on the ask session: %v", err)
	}
	resolved := wAsk.waitForKind(t, string(host.HostElicitResolved))
	var resEv host.HostEvent
	if err := json.Unmarshal(resolved.GetPayload(), &resEv); err != nil {
		t.Fatalf("decode elicit-resolved frame: %v", err)
	}
	if resEv.By != "web" {
		t.Fatalf("ask resolved By = %q, want web", resEv.By)
	}

	select {
	case err := <-submitErr:
		if err != nil {
			t.Fatalf("Submit after ask: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Submit did not finish after the ask was answered")
	}
}

// askGatedApp builds an App whose first turn is a tool call gated by approval
// "ask" mode, with a local elicitation UI that blocks forever, so only a remote
// RespondToAsk can resolve the ask. The second turn finishes the turn.
func askGatedApp(t *testing.T) *host.App {
	t.Helper()
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "do_it"}}, FinishReason: "tool_calls"},
		agent.StubTurn{Text: "done", FinishReason: "stop"},
	)
	blockingUI := func(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
		<-ctx.Done()
		return core.ElicitationResult{}, ctx.Err()
	}
	cfg := &host.Config{
		Model:    host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"},
		Approval: &host.ApprovalConfig{Mode: "ask"},
	}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(stub), host.WithElicitationUI(blockingUI))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

// TestBridge_EmptySessionRoutesToDefault is the backward-compat acceptance: a
// client that sends no session_id drives the default session created at startup,
// exactly as the single-surface flow always has.
func TestBridge_EmptySessionRoutesToDefault(t *testing.T) {
	mgr := NewSessionManager(nil) // no factory: default-only, single-surface shape
	mgr.SetDefault(stubApp(t, agent.StubTurn{Text: "hi", FinishReason: "stop"}))
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := startWatch(t, ctx, client) // empty session_id
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "x"})); err != nil {
		t.Fatalf("Submit (default): %v", err)
	}
	w.waitForKind(t, string(host.HostTurnDone))

	st, err := client.GetStatus(ctx, connect.NewRequest(&agentwebv1.GetStatusRequest{}))
	if err != nil {
		t.Fatalf("GetStatus (default): %v", err)
	}
	if st.Msg.GetModelLabel() != "stub-model" {
		t.Fatalf("GetStatus model = %q, want stub-model", st.Msg.GetModelLabel())
	}

	// The default session is on the roster under its well-known id.
	lw, err := client.ListWebSessions(ctx, connect.NewRequest(&agentwebv1.ListWebSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListWebSessions: %v", err)
	}
	if got := lw.Msg.GetSessionIds(); len(got) != 1 || got[0] != DefaultSessionID {
		t.Fatalf("ListWebSessions = %v, want [%q]", got, DefaultSessionID)
	}
}

// TestBridge_SessionLifecycle covers CreateSession / ListWebSessions /
// CloseSession: a create mints a fresh App on the roster, and a close removes it
// and closes the App so a later request to it is CodeNotFound.
func TestBridge_SessionLifecycle(t *testing.T) {
	mgr := NewSessionManager(func(context.Context) (*host.App, error) { return stubApp(t), nil })
	mgr.SetDefault(stubApp(t))
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Only the default is present up front.
	if got := roster(t, ctx, client); len(got) != 1 || got[0] != DefaultSessionID {
		t.Fatalf("initial roster = %v, want [%q]", got, DefaultSessionID)
	}

	id := mustCreate(t, ctx, client)
	if id == "" || id == DefaultSessionID {
		t.Fatalf("CreateSession minted a bad id: %q", id)
	}

	if got := roster(t, ctx, client); !equalSet(got, []string{DefaultSessionID, id}) {
		t.Fatalf("roster after create = %v, want {%q, %q}", got, DefaultSessionID, id)
	}

	// The created session is a live, fresh App (its status reads back).
	st, err := client.GetStatus(ctx, connect.NewRequest(&agentwebv1.GetStatusRequest{SessionId: id}))
	if err != nil {
		t.Fatalf("GetStatus on created session: %v", err)
	}
	if st.Msg.GetModelLabel() != "stub-model" {
		t.Fatalf("created session model = %q, want stub-model", st.Msg.GetModelLabel())
	}

	// CloseSession removes it from the roster.
	if _, err := client.CloseSession(ctx, connect.NewRequest(&agentwebv1.CloseSessionRequest{SessionId: id})); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if got := roster(t, ctx, client); !equalSet(got, []string{DefaultSessionID}) {
		t.Fatalf("roster after close = %v, want {%q}", got, DefaultSessionID)
	}

	// The closed session no longer routes (App closed and dropped).
	if _, err := client.GetStatus(ctx, connect.NewRequest(&agentwebv1.GetStatusRequest{SessionId: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetStatus on closed session code = %v, want NotFound", connect.CodeOf(err))
	}

	// Closing it again is a no-op miss.
	if _, err := client.CloseSession(ctx, connect.NewRequest(&agentwebv1.CloseSessionRequest{SessionId: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("second CloseSession code = %v, want NotFound", connect.CodeOf(err))
	}
}

// TestBridge_UnknownSession asserts an unknown session_id is CodeNotFound across
// the RPC surface.
func TestBridge_UnknownSession(t *testing.T) {
	mgr := NewSessionManager(nil)
	mgr.SetDefault(stubApp(t))
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := client.GetStatus(ctx, connect.NewRequest(&agentwebv1.GetStatusRequest{SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetStatus unknown code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "x", SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Submit unknown code = %v, want NotFound", connect.CodeOf(err))
	}

	// Watch surfaces the not-found on the first Receive.
	stream, err := client.Watch(ctx, connect.NewRequest(&agentwebv1.WatchRequest{SessionId: "nope"}))
	if err == nil {
		stream.Receive()
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Watch unknown code = %v, want NotFound", connect.CodeOf(err))
	}
}

// TestBridge_CreateSessionWithoutFactory asserts a default-only server (no
// factory) refuses CreateSession with CodeFailedPrecondition.
func TestBridge_CreateSessionWithoutFactory(t *testing.T) {
	mgr := NewSessionManager(nil)
	mgr.SetDefault(stubApp(t))
	srv := httptest.NewServer(HandlerWithSessions(mgr))
	defer srv.Close()
	defer mgr.CloseAll()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := client.CreateSession(ctx, connect.NewRequest(&agentwebv1.CreateSessionRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CreateSession without factory code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

func mustCreate(t *testing.T, ctx context.Context, client agentwebv1connect.HostServiceClient) string {
	t.Helper()
	res, err := client.CreateSession(ctx, connect.NewRequest(&agentwebv1.CreateSessionRequest{}))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return res.Msg.GetSessionId()
}

func roster(t *testing.T, ctx context.Context, client agentwebv1connect.HostServiceClient) []string {
	t.Helper()
	res, err := client.ListWebSessions(ctx, connect.NewRequest(&agentwebv1.ListWebSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListWebSessions: %v", err)
	}
	return res.Msg.GetSessionIds()
}

func equalSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

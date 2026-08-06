package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	agentwebv1 "github.com/panyam/mcpkit/agent/surfaces/web/gen/go/mcpkit/agentweb/v1"
	"github.com/panyam/mcpkit/agent/surfaces/web/gen/go/mcpkit/agentweb/v1/agentwebv1connect"
	"github.com/panyam/mcpkit/core"
)

// recObserver records the HostEvent kinds delivered to a local Observer, so a
// test can assert a web Watch client sees the same stream.
type recObserver struct {
	mu    sync.Mutex
	kinds []host.HostEventKind
}

func (r *recObserver) On(ev host.HostEvent) {
	r.mu.Lock()
	r.kinds = append(r.kinds, ev.Kind)
	r.mu.Unlock()
}

func (r *recObserver) snapshot() []host.HostEventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]host.HostEventKind(nil), r.kinds...)
}

// watcher drives a Watch stream in the background and records frames, so a test
// can wait for a given kind and inspect payloads.
type watcher struct {
	mu     sync.Mutex
	frames []*agentwebv1.Frame
}

func startWatch(t *testing.T, ctx context.Context, client agentwebv1connect.HostServiceClient) *watcher {
	t.Helper()
	stream, err := client.Watch(ctx, connect.NewRequest(&agentwebv1.WatchRequest{}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
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

func (w *watcher) kinds() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.frames))
	for i, f := range w.frames {
		out[i] = f.GetKind()
	}
	return out
}

// waitForKind polls the recorded frames until one of kind appears (returning it)
// or the deadline passes.
func (w *watcher) waitForKind(t *testing.T, kind string) *agentwebv1.Frame {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, f := range w.frames {
			if f.GetKind() == kind {
				w.mu.Unlock()
				return f
			}
		}
		w.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no frame of kind %q arrived; saw %v", kind, w.kinds())
	return nil
}

func contains(kinds []string, want string) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// TestBridge_WatchReplaysAndSubmitRunsTurn is the E3 acceptance: a Connect
// client Submits a turn (which runs on the shared App) and a later Watch client
// replays that turn's events from offset 0, then sees a subsequent turn live —
// the same stream the local Observer receives.
func TestBridge_WatchReplaysAndSubmitRunsTurn(t *testing.T) {
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "first", FinishReason: "stop"},
		agent.StubTurn{Text: "second", FinishReason: "stop"},
	)
	rec := &recObserver{}
	cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
	app, err := host.NewApp(cfg, io.Discard, strings.NewReader(""),
		host.WithProvider(stub), host.WithObserver(rec))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer app.Close()

	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Turn 1 runs over the wire BEFORE any Watch client connects.
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "hi"})); err != nil {
		t.Fatalf("Submit#1: %v", err)
	}

	// A Watch attached now replays turn 1's events from offset 0.
	w := startWatch(t, ctx, client)
	w.waitForKind(t, string(host.HostTurnDone))

	// Turn 2 runs while Watch is live.
	if _, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "again"})); err != nil {
		t.Fatalf("Submit#2: %v", err)
	}

	// The Watch stream must show two finished turns (one replayed, one live).
	deadline := time.Now().Add(3 * time.Second)
	var doneCount int
	for time.Now().Before(deadline) {
		doneCount = 0
		for _, k := range w.kinds() {
			if k == string(host.HostTurnDone) {
				doneCount++
			}
		}
		if doneCount >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if doneCount < 2 {
		t.Fatalf("Watch saw %d turn-done frames, want >= 2 (replay + live); frames=%v", doneCount, w.kinds())
	}

	// The local Observer and the Watch client saw the same stream: every kind the
	// Observer recorded also appears on the wire.
	for _, k := range rec.snapshot() {
		if !contains(w.kinds(), string(k)) {
			t.Fatalf("local Observer saw %q but Watch did not; watch=%v", k, w.kinds())
		}
	}

	// GetStatus is a trivial query over the wire.
	st, err := client.GetStatus(ctx, connect.NewRequest(&agentwebv1.GetStatusRequest{}))
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Msg.GetModelLabel() != "stub-model" {
		t.Fatalf("GetStatus model = %q, want stub-model", st.Msg.GetModelLabel())
	}
}

// TestBridge_RespondToAskWinsOverWire is the multi-surface ask acceptance: a
// turn gates a tool call in approval "ask" mode, the local UI blocks, and a
// RespondToAsk over the wire wins the ask so the turn proceeds — the resolved
// frame names the web surface and the local UI is cancelled.
func TestBridge_RespondToAskWinsOverWire(t *testing.T) {
	// Turn 1 emits a tool call (gated by approval ask); turn 2 finishes.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "do_it"}}, FinishReason: "tool_calls"},
		agent.StubTurn{Text: "done", FinishReason: "stop"},
	)

	// The local elicitation UI blocks forever, so only a remote responder can win.
	localCancelled := make(chan struct{})
	blockingUI := func(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
		<-ctx.Done()
		close(localCancelled)
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
	defer app.Close()

	srv := httptest.NewServer(Handler(app))
	defer srv.Close()
	client := agentwebv1connect.NewHostServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := startWatch(t, ctx, client)

	// Submit runs on its own goroutine because it blocks until the ask resolves.
	submitErr := make(chan error, 1)
	go func() {
		_, err := client.Submit(ctx, connect.NewRequest(&agentwebv1.SubmitRequest{Input: "please act"}))
		submitErr <- err
	}()

	// The ask broadcasts as an elicit-request frame carrying the AskID.
	frame := w.waitForKind(t, string(host.HostElicitRequest))
	var reqEv host.HostEvent
	if err := json.Unmarshal(frame.GetPayload(), &reqEv); err != nil {
		t.Fatalf("decode elicit-request frame: %v", err)
	}
	if reqEv.AskID == 0 {
		t.Fatalf("elicit-request frame carried no AskID: %s", frame.GetPayload())
	}

	// The web surface answers first (confirm=true), winning the ask.
	result, _ := json.Marshal(core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}})
	if _, err := client.RespondToAsk(ctx, connect.NewRequest(&agentwebv1.RespondToAskRequest{
		AskId:  reqEv.AskID,
		Result: result,
		By:     "web",
	})); err != nil {
		t.Fatalf("RespondToAsk: %v", err)
	}

	// The resolved frame names the winning surface.
	resolved := w.waitForKind(t, string(host.HostElicitResolved))
	var resEv host.HostEvent
	if err := json.Unmarshal(resolved.GetPayload(), &resEv); err != nil {
		t.Fatalf("decode elicit-resolved frame: %v", err)
	}
	if resEv.By != "web" {
		t.Fatalf("ask resolved By = %q, want web", resEv.By)
	}

	// The local UI, still blocking, is cancelled because the web surface won.
	select {
	case <-localCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("local elicitation UI was not cancelled after the web surface won")
	}

	// The turn completes now that the ask is answered.
	select {
	case err := <-submitErr:
		if err != nil {
			t.Fatalf("Submit after ask: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Submit did not finish after the ask was answered")
	}
}

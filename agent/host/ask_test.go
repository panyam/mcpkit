package host

import (
	"context"
	"testing"
	"time"

	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/core"
)

func newAskApp() *App {
	rec := &recordObserver{}
	return &App{
		eventLog:  gocurrent.NewQueue[HostEvent](),
		asks:      map[int64]*pendingAsk{},
		observers: []Observer{rec},
	}
}

func askRecorder(a *App) *recordObserver { return a.observers[0].(*recordObserver) }

// waitAskID returns the AskID of the pending HostElicitRequest, waiting for the
// barrier to emit it (barrierElicit runs the awaiting select on the caller's
// goroutine, so a concurrent responder needs the id first).
func waitAskID(t *testing.T, rec *recordObserver) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		for _, e := range rec.evs {
			if e.Kind == HostElicitRequest {
				id := e.AskID
				rec.mu.Unlock()
				return id
			}
		}
		rec.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no HostElicitRequest was emitted")
	return 0
}

func resolvedBy(rec *recordObserver, id int64) (string, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.evs {
		if e.Kind == HostElicitResolved && e.AskID == id {
			return e.By, true
		}
	}
	return "", false
}

// TestAsk_RemoteResponderWinsFirst is the E2 acceptance: with the local UI
// still waiting, another surface answers via RespondToAsk, its answer is
// returned, the local responder is cancelled, and a resolved event names the
// winning surface so other surfaces retract.
func TestAsk_RemoteResponderWinsFirst(t *testing.T) {
	a := newAskApp()
	rec := askRecorder(a)

	localCancelled := make(chan struct{})
	local := func(ctx context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
		<-ctx.Done() // never answers on its own; waits to be cancelled
		close(localCancelled)
		return core.ElicitationResult{}, ctx.Err()
	}
	ui := a.barrierElicit(local)

	type out struct {
		res core.ElicitationResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := ui(context.Background(), core.ElicitationRequest{Message: "approve?"})
		done <- out{res, err}
	}()

	id := waitAskID(t, rec)
	remote := core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}
	if err := a.RespondToAsk(id, remote, "web"); err != nil {
		t.Fatalf("RespondToAsk: %v", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("unexpected error: %v", got.err)
	}
	if got.res.Action != "accept" {
		t.Fatalf("remote answer not returned: %+v", got.res)
	}
	select {
	case <-localCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("local responder was not cancelled after remote won")
	}
	if by, ok := resolvedBy(rec, id); !ok || by != "web" {
		t.Fatalf("resolved event = (%q, %v), want (web, true)", by, ok)
	}
}

// TestAsk_LocalResponderWins pins the single-surface path is unchanged: with no
// other surface attached the local UI answers and its result is returned.
func TestAsk_LocalResponderWins(t *testing.T) {
	a := newAskApp()
	rec := askRecorder(a)

	want := core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}
	local := func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
		return want, nil
	}
	res, err := a.barrierElicit(local)(context.Background(), core.ElicitationRequest{Message: "approve?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "accept" {
		t.Fatalf("local answer not returned: %+v", res)
	}
	id := waitAskID(t, rec)
	if by, ok := resolvedBy(rec, id); !ok || by != "local" {
		t.Fatalf("resolved event = (%q, %v), want (local, true)", by, ok)
	}
}

// TestRespondToAsk_Errors pins the app-state errors a surface reports rather
// than fails on: an unknown ask and an already-answered ask.
func TestRespondToAsk_Errors(t *testing.T) {
	a := newAskApp()

	if err := a.RespondToAsk(999, core.ElicitationResult{}, "web"); err == nil {
		t.Fatal("expected an error responding to an unknown ask")
	}

	id, p := a.registerAsk()
	if !p.resolve(elicitResolution{by: "first"}) {
		t.Fatal("first resolve should win")
	}
	if err := a.RespondToAsk(id, core.ElicitationResult{}, "second"); err == nil {
		t.Fatal("expected an already-answered error")
	}
}

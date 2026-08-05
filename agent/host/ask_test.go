package host

import (
	"context"
	"testing"
	"time"

	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/core"
)

func newAskApp() *App {
	return &App{
		eventLog:  gocurrent.NewQueue[HostEvent](),
		observers: []Observer{&recordObserver{}},
	}
}

func askRecorder(a *App) *recordObserver { return a.observers[0].(*recordObserver) }

// waitAskOffset returns the log offset of the pending HostElicitRequest — the
// ask id a surface reads from its stream position and passes to RespondToAsk.
func waitAskOffset(t *testing.T, a *App) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := a.eventLog.ReadFrom(0)
		for i := range evs {
			if evs[i].Kind == HostElicitRequest {
				return i
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no HostElicitRequest was emitted")
	return 0
}

func resolvedBy(rec *recordObserver, off int) (string, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.evs {
		if e.Kind == HostElicitResolved && e.AskID == int64(off) {
			return e.By, true
		}
	}
	return "", false
}

// TestAsk_RemoteResponderWinsFirst is the E2 acceptance: with the local UI
// still waiting, another surface answers via RespondToAsk on the ask's log
// offset; its answer is returned, the local responder is cancelled, and a
// resolved event names the winning surface so others retract.
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

	off := waitAskOffset(t, a)
	remote := core.ElicitationResult{Action: "accept", Content: map[string]any{"confirm": true}}
	if err := a.RespondToAsk(off, remote, "web"); err != nil {
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
	if by, ok := resolvedBy(rec, off); !ok || by != "web" {
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
	off := waitAskOffset(t, a)
	if by, ok := resolvedBy(rec, off); !ok || by != "local" {
		t.Fatalf("resolved event = (%q, %v), want (local, true)", by, ok)
	}
}

// TestRespondToAsk_Errors pins the app-state errors a surface reports rather
// than fails on, carried straight from the log's barrier: an out-of-range
// offset and an already-answered ask.
func TestRespondToAsk_Errors(t *testing.T) {
	a := newAskApp()

	if err := a.RespondToAsk(999999, core.ElicitationResult{}, "web"); err == nil {
		t.Fatal("expected an error responding to an out-of-range offset")
	}

	off := a.eventLog.Append(HostEvent{Kind: HostElicitRequest})
	if err := a.RespondToAsk(off, core.ElicitationResult{}, "first"); err != nil {
		t.Fatalf("first response should win: %v", err)
	}
	if err := a.RespondToAsk(off, core.ElicitationResult{}, "second"); err == nil {
		t.Fatal("expected an already-answered error")
	}
}

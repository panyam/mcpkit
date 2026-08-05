package host

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/agent"
)

// TestEventLog_BoundedEvicts pins the bounded-log semantics Config.
// MaxEventLogRetention wires: past the retention window the oldest entries
// evict, Len keeps counting the absolute offset space, and ReadFrom(0)
// degrades to the retained suffix (never panics, never renumbers).
func TestEventLog_BoundedEvicts(t *testing.T) {
	a := &App{eventLog: gocurrent.NewBoundedQueue[HostEvent](3)}
	for i := 0; i < 5; i++ {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: strconv.Itoa(i)})
	}
	if got := a.eventLog.Len(); got != 5 {
		t.Fatalf("Len = %d, want 5 (absolute offset space keeps counting past eviction)", got)
	}
	evs, _ := a.eventLog.ReadFrom(0)
	got := make([]string, len(evs))
	for i := range evs {
		got[i] = evs[i].Err
	}
	if want := []string{"2", "3", "4"}; !equalStrings(got, want) {
		t.Fatalf("retained window = %v, want %v (oldest evicted)", got, want)
	}
}

// TestSubscribe_ReplayThenLive is the E3 async-seam acceptance: a consumer that
// attaches after events were emitted replays them from offset 0 and then
// receives live events, until ctx is cancelled. This is what a web Watch RPC
// drains onto the wire.
func TestSubscribe_ReplayThenLive(t *testing.T) {
	a := &App{eventLog: gocurrent.NewQueue[HostEvent]()}
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "0"})
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := a.Subscribe(ctx)

	// Backlog replays from offset 0.
	if got := recvErr(t, ch); got != "0" {
		t.Fatalf("replay[0] = %q, want %q", got, "0")
	}
	if got := recvErr(t, ch); got != "1" {
		t.Fatalf("replay[1] = %q, want %q", got, "1")
	}

	// A live event after Subscribe is delivered too.
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "2"})
	if got := recvErr(t, ch); got != "2" {
		t.Fatalf("live = %q, want %q", got, "2")
	}

	// Cancelling ctx closes the channel and stops the drain goroutine.
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// One buffered event may drain before close; read until closed.
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatal("channel not closed after ctx cancel")
				}
			case <-time.After(time.Second):
				t.Fatal("channel not closed after ctx cancel")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after ctx cancel")
	}
}

// TestSubscribe_LocalObserverAndSubscriberSeeSameStream pins that Subscribe is a
// faithful second view of the one substrate: a synchronous local Observer and
// an async Subscribe consumer receive the identical event sequence, so a TUI
// (Observer) and a browser (Subscribe) on one App see the same turns.
func TestSubscribe_LocalObserverAndSubscriberSeeSameStream(t *testing.T) {
	rec := &recordObserver{}
	a := &App{eventLog: gocurrent.NewQueue[HostEvent](), observers: []Observer{rec}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := a.Subscribe(ctx)

	want := []HostEventKind{HostRunnerEvent, HostTurnDone, HostSessionWarn}
	for _, k := range want {
		a.emit(HostEvent{Kind: k})
	}

	for i, k := range want {
		select {
		case ev := <-ch:
			if ev.Kind != k {
				t.Fatalf("subscriber[%d] = %q, want %q", i, ev.Kind, k)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber timed out at %d", i)
		}
	}
	if got := rec.kinds(); len(got) != len(want) {
		t.Fatalf("observer saw %d events, want %d", len(got), len(want))
	}
}

// TestSubscribe_NoStoreReplaysRetainedWindow is acceptance (c): without a
// RunStore, Subscribe replays the current retained window and then follows
// live. With a bounded log the window is the un-evicted suffix.
func TestSubscribe_NoStoreReplaysRetainedWindow(t *testing.T) {
	a := &App{eventLog: gocurrent.NewBoundedQueue[HostEvent](2)}
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "0"}) // evicted before Subscribe
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "1"})
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "2"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := a.Subscribe(ctx)

	if got := recvErr(t, ch); got != "1" {
		t.Fatalf("first replayed = %q, want %q (0 evicted)", got, "1")
	}
	if got := recvErr(t, ch); got != "2" {
		t.Fatalf("second replayed = %q, want %q", got, "2")
	}
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "3"}) // live
	if got := recvErr(t, ch); got != "3" {
		t.Fatalf("live = %q, want %q", got, "3")
	}
}

// TestSubscribe_DeepReplayAfterEviction is the headline acceptance (b): with a
// small retention the early turns evict from the in-memory Queue, yet a
// subscriber attaching afterward replays the FULL conversation — deep history
// from the RunStore stitched with the live tail — with no duplicated and no
// missing turn. It also exercises the NewApp bounded-queue wiring.
func TestSubscribe_DeepReplayAfterEviction(t *testing.T) {
	ctx := context.Background()
	ts := startTestServer(t)
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "t1"},
		agent.StubTurn{Text: "t2"},
		agent.StubTurn{Text: "t3"},
	)
	cfg := testConfig(ts.URL)
	cfg.MaxEventLogRetention = 4 // small enough that the first turns evict
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), WithProvider(stub), WithRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	for _, in := range []string{"a", "b", "c"} {
		if err := app.RunTurn(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	// Eviction really happened: the log counted more than the window, and the
	// retained window is capped, so the early turns are gone from the Queue.
	if n := app.eventLog.Len(); n <= 4 {
		t.Fatalf("expected the log to exceed the retention window, Len = %d", n)
	}
	if evs, _ := app.eventLog.ReadFrom(0); len(evs) > 4 {
		t.Fatalf("retained window = %d entries, want <= 4 (evicting)", len(evs))
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := app.Subscribe(subCtx)

	text, turnEnds := collectReplay(t, ch, 3)
	if turnEnds != 3 {
		t.Fatalf("replayed %d turn-ends, want 3 (no missing turn)", turnEnds)
	}
	if text != "t1t2t3" {
		t.Fatalf("replayed text deltas = %q, want %q (in order, no dup, no gap)", text, "t1t2t3")
	}
}

// TestPersistedOffsetAdvancesAtTurnEnd is acceptance (d): persistedOffset is
// zero before any persisted turn and advances once the turn's events land in
// the RunStore, staying within the event log's length.
func TestPersistedOffsetAdvancesAtTurnEnd(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(agent.StubTurn{Text: "answer"})
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), io.Discard, strings.NewReader(""), WithProvider(stub), WithRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	if got := app.persistedOffset.Load(); got != 0 {
		t.Fatalf("persistedOffset before any turn = %d, want 0", got)
	}
	if err := app.RunTurn(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	off := app.persistedOffset.Load()
	if off == 0 {
		t.Fatal("persistedOffset did not advance after a persisted turn")
	}
	if n := int64(app.eventLog.Len()); off > n {
		t.Fatalf("persistedOffset = %d exceeds event log length %d", off, n)
	}
}

// collectReplay drains the subscription, tallying replayed text deltas and
// turn-ends, until wantTurnEnds turn-ends are seen or a timeout fires. Only
// runner events matter here; the non-runner tail (HostTurnDone) is ignored.
func collectReplay(t *testing.T, ch <-chan HostEvent, wantTurnEnds int) (text string, turnEnds int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for turnEnds < wantTurnEnds {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Kind != HostRunnerEvent {
				continue
			}
			switch ev.RunnerEvent.Kind {
			case agent.EventTextDelta:
				text += ev.RunnerEvent.Text
			case agent.EventTurnEnd:
				turnEnds++
			}
		case <-deadline:
			t.Fatalf("timed out draining subscription: text=%q turnEnds=%d", text, turnEnds)
		}
	}
	return
}

func recvErr(t *testing.T, ch <-chan HostEvent) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev.Err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a subscription event")
		return ""
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

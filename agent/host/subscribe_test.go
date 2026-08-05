package host

import (
	"context"
	"testing"
	"time"

	gocurrent "github.com/panyam/gocurrent"
)

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

func recvErr(t *testing.T, ch <-chan HostEvent) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev.Err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return ""
	}
}

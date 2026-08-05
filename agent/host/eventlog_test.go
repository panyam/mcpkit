package host

import (
	"reflect"
	"testing"

	gocurrent "github.com/panyam/gocurrent"
)

// TestEventLog_RetainsForLateReplay is the E1 substrate acceptance: emit
// records every event on the retained log, so a subscriber that attaches after
// events were emitted replays them from offset 0 and is notified of live ones.
// This is what lets a web surface (issue 1196) join a running session and see
// its history.
func TestEventLog_RetainsForLateReplay(t *testing.T) {
	a := &App{eventLog: gocurrent.NewQueue[HostEvent]()}
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "0"})
	a.emit(HostEvent{Kind: HostSessionWarn, Err: "1"})

	sub := a.eventLog.Subscribe() // subscribes late, after "0" and "1"
	defer sub.Close()

	a.emit(HostEvent{Kind: HostSessionWarn, Err: "2"})

	select {
	case <-sub.Notify():
	default:
		t.Fatal("late subscriber was not notified of the live event")
	}

	evs, _ := a.eventLog.ReadFrom(0)
	got := make([]string, len(evs))
	for i := range evs {
		got[i] = evs[i].Err
	}
	if want := []string{"0", "1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replay = %v, want %v", got, want)
	}
}

// TestEmit_DeliversToLocalObserversSynchronously pins the terminal rendering
// contract that the log must not break: emit delivers to registered observers
// synchronously and in order (the event is present the instant emit returns, no
// wait), and the same events land on the retained log.
func TestEmit_DeliversToLocalObserversSynchronously(t *testing.T) {
	rec := &recordObserver{}
	a := &App{eventLog: gocurrent.NewQueue[HostEvent](), observers: []Observer{rec}}

	want := []HostEventKind{HostRunnerEvent, HostTurnDone, HostSessionWarn}
	for _, k := range want {
		a.emit(HostEvent{Kind: k})
	}

	if got := rec.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("observer order = %v, want %v", got, want)
	}
	if n := a.eventLog.Len(); n != len(want) {
		t.Fatalf("retained log length = %d, want %d", n, len(want))
	}
}

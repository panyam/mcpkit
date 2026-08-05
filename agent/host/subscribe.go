package host

import "context"

// subscribeBuffer smooths bursts on a Subscribe channel. It is not a
// correctness bound: the drain goroutine reads the retained event log at its
// own pace, so even a full buffer only slows the drain, never emit.
const subscribeBuffer = 64

// Subscribe delivers HostEvents to a remote or async consumer: the retained
// backlog replayed from offset 0, then live events, until ctx is cancelled.
// Local observers stay synchronous (issue 1194, emit's in-line fan-out); this
// is the async seam a web surface (issue 1196) subscribes onto, kept separate
// so a slow remote consumer never stalls the terminal rendering contract.
//
// A slow consumer cannot block emit. emit only Appends to the retained log
// (non-blocking) and fans out to the local observers; this drain goroutine
// reads the log independently via ReadFrom and blocks only itself when the
// consumer falls behind. Because the log is retained (Option A, unbounded for
// now), nothing is lost and back-pressure stays contained to the one slow
// subscriber. That decoupling is the whole reason the log is the substrate.
//
// The returned channel is closed once ctx is done and the drain goroutine has
// exited, so a caller can range over it and clean up on close. The consumer
// owns the lifetime through ctx: cancel it (a Watch RPC does so on disconnect)
// or the goroutine and its Subscription leak. A dropped consumer that wants the
// stream again simply calls Subscribe once more and replays from offset 0.
func (a *App) Subscribe(ctx context.Context) <-chan HostEvent {
	out := make(chan HostEvent, subscribeBuffer)
	sub := a.eventLog.Subscribe()
	go func() {
		defer close(out)
		defer sub.Close()
		offset := 0
		for {
			events, next := a.eventLog.ReadFrom(offset)
			for i, ev := range events {
				// The ask id is the event-log offset the ask was announced at
				// (E2's offset-based RespondToAsk resolves that offset via the
				// log barrier). HostElicitRequest is emitted without an id, so
				// stamp the offset onto the delivered copy here — HostEvent is a
				// value, so this never mutates the stored log entry — letting a
				// remote surface read the id off the frame and answer.
				if ev.Kind == HostElicitRequest {
					ev.AskID = int64(offset + i)
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
			offset = next
			select {
			case <-sub.Notify():
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

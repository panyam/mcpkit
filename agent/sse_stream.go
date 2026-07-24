package agent

import (
	"context"
	"errors"
	"io"
	"strings"

	ssehttp "github.com/panyam/servicekit/http"
)

// sseStream is the shared read loop for the streaming providers. Both the
// OpenAI and Anthropic streams landed as near-identical Recv() bodies — queue
// drain, done check, ctx cancellation, ReadEvent, the subtle partial-event-
// riding-EOF handling, TrimSpace — differing only in how one SSE data payload
// decodes into Deltas. Consolidating the loop here removes that bug-drift risk;
// each provider embeds sseStream and supplies its own decode.
type sseStream struct {
	ctx    context.Context
	body   io.ReadCloser
	events *ssehttp.SSEEventReader
	queue  []Delta
	done   bool

	// decode turns one non-empty, trimmed SSE data payload into zero or more
	// Deltas. done=true ends the stream on the provider's terminal sentinel
	// (OpenAI's "[DONE]", Anthropic's "message_stop"); a decode error surfaces
	// from Recv. It may keep per-stream state across calls (OpenAI's tool-call
	// index map), so it is a closure/method bound to the concrete stream.
	decode func(payload string) (deltas []Delta, done bool, err error)
}

// Recv implements Stream. It returns queued Deltas one at a time, reading and
// decoding the next SSE event only when the queue is empty. A partial event can
// ride along with io.EOF; it is decoded before EOF is surfaced on the next
// call. A cancelled context takes precedence over a read error.
func (s *sseStream) Recv() (Delta, error) {
	for {
		if len(s.queue) > 0 {
			d := s.queue[0]
			s.queue = s.queue[1:]
			return d, nil
		}
		if s.done {
			return Delta{}, io.EOF
		}
		if err := s.ctx.Err(); err != nil {
			return Delta{}, err
		}
		ev, err := s.events.ReadEvent()
		if err != nil {
			if ctxErr := s.ctx.Err(); ctxErr != nil {
				return Delta{}, ctxErr
			}
			// A partial event can ride along with io.EOF; process it before
			// surfacing EOF on the next call.
			if !errors.Is(err, io.EOF) || ev.Data == "" {
				return Delta{}, err
			}
			s.done = true
		}
		payload := strings.TrimSpace(ev.Data)
		if payload == "" {
			continue
		}
		deltas, done, derr := s.decode(payload)
		if derr != nil {
			return Delta{}, derr
		}
		if done {
			s.done = true
		}
		s.queue = append(s.queue, deltas...)
	}
}

// Close implements Stream: it closes the underlying response body.
func (s *sseStream) Close() error { return s.body.Close() }

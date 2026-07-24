package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	ssehttp "github.com/panyam/servicekit/http"
)

func newTestSSEStream(ctx context.Context, data string, decode func(string) ([]Delta, bool, error)) *sseStream {
	r := io.NopCloser(strings.NewReader(data))
	return &sseStream{ctx: ctx, body: r, events: ssehttp.NewSSEEventReader(r), decode: decode}
}

// TestSSEStream_DoneSentinelStopsBeforeLaterEvents pins the shared loop: a
// decode returning done=true ends the stream, so events after the sentinel are
// never decoded, and Recv then returns io.EOF.
func TestSSEStream_DoneSentinelStopsBeforeLaterEvents(t *testing.T) {
	s := newTestSSEStream(context.Background(),
		"data: one\n\ndata: two\n\ndata: STOP\n\ndata: three\n\n",
		func(p string) ([]Delta, bool, error) {
			if p == "STOP" {
				return nil, true, nil
			}
			return []Delta{{Kind: DeltaText, Text: p}}, false, nil
		})

	got := collectDeltas(t, s)
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "two" {
		t.Fatalf("deltas after the done sentinel leaked: %+v", got)
	}
}

// TestSSEStream_EmptyPayloadSkipped pins that a blank data line yields no delta
// and does not end the stream.
func TestSSEStream_EmptyPayloadSkipped(t *testing.T) {
	s := newTestSSEStream(context.Background(),
		"data: \n\ndata: hi\n\n",
		func(p string) ([]Delta, bool, error) { return []Delta{{Kind: DeltaText, Text: p}}, false, nil })
	got := collectDeltas(t, s)
	if len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("empty payload not skipped: %+v", got)
	}
}

// TestSSEStream_DecodeErrorSurfaces pins that a decode error is returned from
// Recv (not swallowed).
func TestSSEStream_DecodeErrorSurfaces(t *testing.T) {
	boom := errors.New("bad chunk")
	s := newTestSSEStream(context.Background(),
		"data: x\n\n",
		func(string) ([]Delta, bool, error) { return nil, false, boom })
	if _, err := s.Recv(); !errors.Is(err, boom) {
		t.Fatalf("Recv err = %v, want the decode error", err)
	}
}

// TestSSEStream_ContextCancelTakesPrecedence pins that a cancelled context ends
// Recv with the context error rather than decoding further.
func TestSSEStream_ContextCancelTakesPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newTestSSEStream(ctx,
		"data: x\n\n",
		func(string) ([]Delta, bool, error) { return []Delta{{Kind: DeltaText, Text: "x"}}, false, nil })
	if _, err := s.Recv(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v, want context.Canceled", err)
	}
}

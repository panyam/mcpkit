package eventsclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPoll_ResumesFromCursorAgainstRealServer runs the loop against the
// library's own events/poll handler. A first loop starting "from now" leaves a
// cursor; an event yielded after it stops is delivered by a second loop that
// resumes from that cursor, on its first poll. Both loops end cleanly on Stop.
func TestPoll_ResumesFromCursorAgainstRealServer(t *testing.T) {
	c, yield, _ := stack(t)

	first, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{EventName: "fake.event"})
	require.NoError(t, err)
	resume := first.Cursor()
	require.NotNil(t, resume, "a cursored source leaves a cursor to resume from")
	first.Stop()
	<-first.Done()
	assert.NoError(t, first.Err())

	require.NoError(t, yield(t.Context(), fakePayload{Msg: "one"}))

	got := make(chan events.Event, 4)
	second, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{
		EventName: "fake.event",
		Cursor:    resume,
		OnEvent:   func(ev events.Event) { got <- ev },
	})
	require.NoError(t, err)
	defer second.Stop()
	select {
	case ev := <-got:
		assert.Equal(t, "fake.event", ev.Name)
	case <-time.After(5 * time.Second):
		t.Fatal("event yielded after the first loop was not replayed from its cursor")
	}
}

// TestPoll_RejectionSurfacesFromConstructor checks that a first-poll error
// comes back from Poll itself rather than disappearing into the goroutine.
func TestPoll_RejectionSurfacesFromConstructor(t *testing.T) {
	c, _, _ := stack(t)
	_, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{EventName: "no.such.event"})
	assert.Error(t, err)
}

// scriptedPoll is one canned events/poll answer. listChanged sends the
// response as SSE with notifications/events/list_changed ahead of it.
type scriptedPoll struct {
	result      map[string]any
	listChanged bool
}

type seenPoll struct {
	at     time.Time
	cursor any
}

// scriptedServer answers initialize and then each events/poll from script,
// repeating the last entry, and records when each poll arrived and which
// cursor it carried.
func scriptedServer(t *testing.T, script []scriptedPoll) (*client.Client, func() []seenPoll) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenPoll

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		reply := func(result any) map[string]any {
			return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply(map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/events": map[string]any{}}},
				"serverInfo":      map[string]any{"name": "scripted", "version": "0"},
			}))
		case "events/poll":
			var params map[string]any
			_ = json.Unmarshal(req.Params, &params)
			mu.Lock()
			step := min(len(seen), len(script)-1)
			seen = append(seen, seenPoll{at: time.Now(), cursor: params["cursor"]})
			mu.Unlock()
			s := script[step]
			if s.listChanged {
				w.Header().Set("Content-Type", "text/event-stream")
				note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/events/list_changed"})
				res, _ := json.Marshal(reply(s.result))
				_, _ = w.Write([]byte("data: " + string(note) + "\n\ndata: " + string(res) + "\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply(s.result))
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply(map[string]any{}))
		}
	}))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL, core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect(t.Context()))
	return c, func() []seenPoll {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenPoll(nil), seen...)
	}
}

func runPoll(t *testing.T, c *client.Client, opts eventsclient.PollOptions, until func() bool) {
	t.Helper()
	opts.EventName = "scripted.event"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	p, err := eventsclient.Poll(ctx, c, opts)
	require.NoError(t, err)
	defer p.Stop()
	require.Eventually(t, until, 8*time.Second, 10*time.Millisecond)
}

// TestPoll_DrainsOnHasMoreThenHonoursFloor: hasMore skips the wait even when
// nextPollMs is large, and nextPollMs 0 still waits the floor.
func TestPoll_DrainsOnHasMoreThenHonoursFloor(t *testing.T) {
	c, seen := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{}, "cursor": "c1", "hasMore": true, "nextPollMs": 60000}},
		{result: map[string]any{"events": []any{}, "cursor": "c2", "hasMore": false, "nextPollMs": 0}},
		{result: map[string]any{"events": []any{}, "cursor": "c3", "hasMore": false, "nextPollMs": 0}},
	})
	floor := 300 * time.Millisecond
	runPoll(t, c, eventsclient.PollOptions{Floor: floor}, func() bool { return len(seen()) >= 3 })

	s := seen()
	assert.Less(t, s[1].at.Sub(s[0].at), floor, "hasMore should poll again at once")
	assert.GreaterOrEqual(t, s[2].at.Sub(s[1].at), floor, "nextPollMs 0 must still wait the floor")
	assert.Equal(t, "c1", s[1].cursor)
	assert.Equal(t, "c2", s[2].cursor)
}

// TestPoll_HonoursNextPollMsAboveFloor: a longer nextPollMs wins over the floor.
func TestPoll_HonoursNextPollMsAboveFloor(t *testing.T) {
	c, seen := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{}, "cursor": "c1", "hasMore": false, "nextPollMs": 400}},
	})
	runPoll(t, c, eventsclient.PollOptions{Floor: 50 * time.Millisecond}, func() bool { return len(seen()) >= 2 })
	s := seen()
	assert.GreaterOrEqual(t, s[1].at.Sub(s[0].at), 400*time.Millisecond)
}

// TestPoll_AdoptsFreshCursorOnTruncated: the cursor that comes with
// truncated: true is the one the next poll sends, and OnTruncated sees it.
func TestPoll_AdoptsFreshCursorOnTruncated(t *testing.T) {
	c, seen := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{}, "cursor": "fresh", "truncated": true, "hasMore": false, "nextPollMs": 0}},
	})
	var truncatedAt *string
	var mu sync.Mutex
	runPoll(t, c, eventsclient.PollOptions{
		Floor: 50 * time.Millisecond,
		OnTruncated: func(cur *string) {
			mu.Lock()
			truncatedAt = cur
			mu.Unlock()
		},
	}, func() bool { return len(seen()) >= 2 })

	assert.Equal(t, "fresh", seen()[1].cursor)
	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, truncatedAt)
	assert.Equal(t, "fresh", *truncatedAt)
}

// TestPoll_NullAndAbsentCursorsAreNotReplayed: after `cursor: null`, and
// after a response with no cursor at all, the loop keeps polling with null
// and never substitutes the eventId it just saw.
func TestPoll_NullAndAbsentCursorsAreNotReplayed(t *testing.T) {
	ev := map[string]any{"eventId": "evt-1", "name": "scripted.event", "timestamp": "2026-10-03T00:00:00Z", "data": map[string]any{}, "cursor": nil}
	c, seen := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{ev}, "cursor": nil, "hasMore": false, "nextPollMs": 0}},
		{result: map[string]any{"events": []any{}, "hasMore": false, "nextPollMs": 0}},
	})
	runPoll(t, c, eventsclient.PollOptions{Floor: 50 * time.Millisecond}, func() bool { return len(seen()) >= 3 })
	for i, s := range seen() {
		assert.Nil(t, s.cursor, "poll %d replayed a cursor", i)
	}
}

// TestPoll_SurfacesListChanged: a list_changed notification on a poll
// response reaches OnListChanged.
func TestPoll_SurfacesListChanged(t *testing.T) {
	c, _ := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{}, "cursor": "c1", "hasMore": false, "nextPollMs": 0}, listChanged: true},
	})
	var fired sync.Once
	ch := make(chan struct{})
	runPoll(t, c, eventsclient.PollOptions{
		Floor:         50 * time.Millisecond,
		OnListChanged: func() { fired.Do(func() { close(ch) }) },
	}, func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	})
}

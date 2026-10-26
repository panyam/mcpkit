package eventsclient_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const dedupSecret = "test-secret"

// postSigned sends one X-MCP-signed event delivery to recv, timestamped at,
// and returns the status code.
func postSigned(t *testing.T, recv http.Handler, eventID, subID string, at time.Time) int {
	t.Helper()
	body := `{"eventId":"` + eventID + `","name":"fake.event","timestamp":"2026-10-22T00:00:00Z","data":{"msg":"hi"},"cursor":"c"}`
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(dedupSecret))
	mac.Write([]byte(ts + "." + body))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-MCP-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-MCP-Timestamp", ts)
	if subID != "" {
		req.Header.Set("X-MCP-Subscription-Id", subID)
	}
	rec := httptest.NewRecorder()
	recv.ServeHTTP(rec, req)
	return rec.Code
}

func drain[D any](r *eventsclient.Receiver[D]) int {
	n := 0
	for {
		select {
		case <-r.Events():
			n++
		default:
			return n
		}
	}
}

func TestReceiver_DropsRetriedDelivery(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
	defer recv.Close()

	assert.Equal(t, http.StatusOK, postSigned(t, recv, "evt-1", "sub-a", time.Now()))
	assert.Equal(t, http.StatusOK, postSigned(t, recv, "evt-1", "sub-a", time.Now()), "a retry is acknowledged so the server stops retrying")
	assert.Equal(t, 1, drain(recv))
}

// One event matching two subscriptions to the same URL arrives once per
// subscription, with the same webhook-id. Keying on eventId alone would drop
// the second subscription's copy.
func TestReceiver_DeliversOncePerSubscription(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
	defer recv.Close()

	postSigned(t, recv, "evt-1", "sub-a", time.Now())
	postSigned(t, recv, "evt-1", "sub-b", time.Now())
	assert.Equal(t, 2, drain(recv))
}

func TestReceiver_WithoutDedupDeliversRetries(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret, eventsclient.WithoutDedup())
	defer recv.Close()

	postSigned(t, recv, "evt-1", "sub-a", time.Now())
	postSigned(t, recv, "evt-1", "sub-a", time.Now())
	assert.Equal(t, 2, drain(recv))
}

// An event dropped on a full channel was never handed to the application, so
// the server's retry of it must get through.
func TestReceiver_DeliversRetryOfDroppedEvent(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
	defer recv.Close()

	for i := range 64 {
		require.Equal(t, http.StatusOK, postSigned(t, recv, "fill-"+strconv.Itoa(i), "", time.Now()))
	}
	assert.Equal(t, http.StatusServiceUnavailable, postSigned(t, recv, "evt-late", "", time.Now()),
		"a dropped event is answered 503, which the server retries, not 200")
	require.Equal(t, uint64(1), recv.Rejected())

	<-recv.Events()
	postSigned(t, recv, "evt-late", "", time.Now())
	assert.Equal(t, uint64(1), recv.Rejected())
	assert.Equal(t, 64, drain(recv))
}

func TestReceiver_RefusesStaleTimestamp(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
	defer recv.Close()

	assert.Equal(t, http.StatusUnauthorized, postSigned(t, recv, "evt-old", "", time.Now().Add(-6*time.Minute)))
	assert.Equal(t, http.StatusUnauthorized, postSigned(t, recv, "evt-future", "", time.Now().Add(6*time.Minute)))
	assert.Equal(t, http.StatusOK, postSigned(t, recv, "evt-recent", "", time.Now().Add(-4*time.Minute)))
	assert.Equal(t, 1, drain(recv))
}

func TestReceiver_TimestampToleranceZeroDisablesCheck(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret, eventsclient.WithTimestampTolerance(0))
	defer recv.Close()

	assert.Equal(t, http.StatusOK, postSigned(t, recv, "evt-old", "", time.Now().Add(-time.Hour)))
}

// The overlap #1581 hits: an occurrence pushed on a stream comes back in the
// poll backfill that resumes from a cursor taken before it. One Deduper shared
// across both delivers it once.
func TestDeduper_SharedAcrossStreamAndPollBackfill(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
		want  int32
	}{
		{"shared deduper", false, 1},
		{"duplicates allowed", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, yield := streamStack(t, time.Hour)
			var n atomic.Int32
			count := func(events.Event) { n.Add(1) }
			d := eventsclient.NewDeduper(0, 0)

			head, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{EventName: "fake.event"})
			require.NoError(t, err)
			before := head.Cursor()
			head.Stop()

			stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{
				EventName: "fake.event", OnEvent: count, Dedup: d, AllowDuplicates: tc.allow,
			})
			require.NoError(t, err)
			defer stream.Stop()

			require.NoError(t, yield(t.Context(), fakePayload{Msg: "once"}))
			require.Eventually(t, func() bool { return n.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

			backfill, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{
				EventName: "fake.event", Cursor: before, OnEvent: count, Dedup: d, AllowDuplicates: tc.allow,
			})
			require.NoError(t, err)
			backfill.Stop()
			assert.Equal(t, tc.want, n.Load())
		})
	}
}

func TestPoll_DropsEventRepeatedAcrossPages(t *testing.T) {
	ev := map[string]any{"eventId": "evt-1", "name": "scripted.event", "timestamp": "2026-10-22T00:00:00Z", "data": map[string]any{}, "cursor": "c1"}
	c, seen := scriptedServer(t, []scriptedPoll{
		{result: map[string]any{"events": []any{ev}, "cursor": "c1", "hasMore": true}},
		{result: map[string]any{"events": []any{ev}, "cursor": "c1", "hasMore": false}},
	})
	var n atomic.Int32
	runPoll(t, c, eventsclient.PollOptions{
		Floor:   20 * time.Millisecond,
		OnEvent: func(events.Event) { n.Add(1) },
	}, func() bool { return len(seen()) >= 3 })
	assert.Equal(t, int32(1), n.Load())
}

func TestTimestampWithinTolerance(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	assert.True(t, events.TimestampWithinTolerance(ts(0), now, 5*time.Minute))
	assert.True(t, events.TimestampWithinTolerance(ts(-5*time.Minute), now, 5*time.Minute))
	assert.False(t, events.TimestampWithinTolerance(ts(-5*time.Minute-time.Second), now, 5*time.Minute))
	assert.False(t, events.TimestampWithinTolerance(ts(5*time.Minute+time.Second), now, 5*time.Minute))
	assert.False(t, events.TimestampWithinTolerance("", now, 5*time.Minute))
	assert.False(t, events.TimestampWithinTolerance("not-a-number", now, 5*time.Minute))
}

// Close racing in-flight deliveries used to panic with "send on closed
// channel": the closed check and the send were not under one lock.
func TestReceiver_CloseDuringDeliveryDoesNotPanic(t *testing.T) {
	for range 200 {
		recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				postSigned(t, recv, "evt-"+strconv.Itoa(i), "", time.Now())
			}()
		}
		recv.Close()
		wg.Wait()
	}
}

package events

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Conformance suite for EventBufferStore. Run via newInMemoryStore in
// this file; stores/gorm reuses the same body against sqlite + real
// Postgres so every backend agrees on the seam's behavior.

func ev(cursor string, eventID string) Event {
	c := cursor
	return Event{
		EventID:   eventID,
		Name:      "test.event",
		Timestamp: "2026-06-09T00:00:00Z",
		Cursor:    &c,
		Data:      []byte(`{}`),
	}
}

// runEventBufferConformance runs the full conformance matrix against
// any EventBufferStore. The maxSize argument tells the test what size
// the store was constructed with — needed so the eviction-shape tests
// know how many Appends to do before checking Truncated.
//
// gorm's test file invokes this same function with a Postgres-backed
// store + the same maxSize the gorm impl was configured with.
func runEventBufferConformance(t *testing.T, store EventBufferStore, maxSize int) {
	t.Helper()
	ctx := t.Context()

	t.Run("EmptyPollOnEmptyBufferReturnsNothing", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "empty-src"})
		resp, err := store.Poll(ctx, PollEventsRequest{SourceName: "empty-src", Cursor: "", Limit: 10})
		require.NoError(t, err)
		assert.Empty(t, resp.Events)
		assert.Equal(t, "", resp.NextCursor)
		assert.False(t, resp.Truncated)
	})

	t.Run("EmptyCursorReturnsEventsFromStart", func(t *testing.T) {
		// Matches YieldingSource.Poll's historical behavior: empty
		// cursor parses as 0; events with cursor > 0 are returned.
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src1"})
		_, err := store.Append(ctx, AppendEventRequest{SourceName: "src1", Event: ev("1", "e1")})
		require.NoError(t, err)
		resp, err := store.Poll(ctx, PollEventsRequest{SourceName: "src1", Cursor: "", Limit: 10})
		require.NoError(t, err)
		require.Len(t, resp.Events, 1)
		assert.Equal(t, "e1", resp.Events[0].EventID)
		assert.Equal(t, "1", resp.NextCursor)
	})

	t.Run("PollWithCursorReturnsLaterEvents", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src2"})
		for i := 1; i <= 5; i++ {
			_, err := store.Append(ctx, AppendEventRequest{
				SourceName: "src2", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
			require.NoError(t, err)
		}
		resp, err := store.Poll(ctx, PollEventsRequest{SourceName: "src2", Cursor: "2", Limit: 10})
		require.NoError(t, err)
		require.Len(t, resp.Events, 3, "events 3, 4, 5 should be returned")
		assert.Equal(t, "5", resp.NextCursor)
		assert.False(t, resp.Truncated)
	})

	t.Run("PollLimitClamps", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src3"})
		for i := 1; i <= 5; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src3", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		resp, _ := store.Poll(ctx, PollEventsRequest{SourceName: "src3", Cursor: "0", Limit: 2})
		require.Len(t, resp.Events, 2)
		assert.Equal(t, "2", resp.NextCursor)
	})

	t.Run("PollPastLatestReturnsNothingButCursorAtLatest", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "past-latest"})
		_, _ = store.Append(ctx, AppendEventRequest{
			SourceName: "past-latest", Event: ev("1", "e1"),
		})
		resp, _ := store.Poll(ctx, PollEventsRequest{SourceName: "past-latest", Cursor: "99", Limit: 10})
		assert.Empty(t, resp.Events)
		assert.Equal(t, "1", resp.NextCursor, "no events matched but buffer has entries → NextCursor = Latest")
	})

	t.Run("LatestReportsMostRecent", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src4"})
		empty, _ := store.Latest(ctx, LatestCursorRequest{SourceName: "src4"})
		assert.Equal(t, "", empty.Cursor, "Latest on empty source is empty")
		for i := 1; i <= 3; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src4", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		latest, _ := store.Latest(ctx, LatestCursorRequest{SourceName: "src4"})
		assert.Equal(t, "3", latest.Cursor)
	})

	t.Run("RecentReturnsOldestFirst", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src5"})
		for i := 1; i <= 5; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src5", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		resp, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "src5", N: 3})
		require.Len(t, resp.Events, 3)
		assert.Equal(t, "e3", resp.Events[0].EventID, "oldest in the tail")
		assert.Equal(t, "e5", resp.Events[2].EventID, "newest at the end")
	})

	t.Run("RecentClampsToBufferSize", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src6"})
		for i := 1; i <= 2; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src6", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		resp, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "src6", N: 10})
		assert.Len(t, resp.Events, 2)
	})

	t.Run("RecentEmptyOnZeroOrNegative", func(t *testing.T) {
		resp, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "src7", N: 0})
		assert.Empty(t, resp.Events)
		resp, _ = store.Recent(ctx, RecentEventsRequest{SourceName: "src7", N: -1})
		assert.Empty(t, resp.Events)
	})

	t.Run("SourceIsolation", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "iso-a"})
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "iso-b"})
		_, _ = store.Append(ctx, AppendEventRequest{SourceName: "iso-a", Event: ev("1", "a1")})
		_, _ = store.Append(ctx, AppendEventRequest{SourceName: "iso-b", Event: ev("1", "b1")})
		latestA, _ := store.Latest(ctx, LatestCursorRequest{SourceName: "iso-a"})
		recentB, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "iso-b", N: 10})
		assert.Equal(t, "1", latestA.Cursor)
		require.Len(t, recentB.Events, 1)
		assert.Equal(t, "b1", recentB.Events[0].EventID, "iso-b's Recent must not contain iso-a's events")
	})

	t.Run("TruncateBeforeCursor", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src8"})
		for i := 1; i <= 5; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src8", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		resp, err := store.Truncate(ctx, TruncateEventsRequest{SourceName: "src8", BeforeCursor: "3"})
		require.NoError(t, err)
		assert.Equal(t, 3, resp.Removed, "events 1, 2, 3 dropped")
		recent, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "src8", N: 10})
		require.Len(t, recent.Events, 2)
		assert.Equal(t, "e4", recent.Events[0].EventID)
	})

	t.Run("TruncateEmptyDropsAll", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src9"})
		for i := 1; i <= 3; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src9", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src9"})
		latest, _ := store.Latest(ctx, LatestCursorRequest{SourceName: "src9"})
		assert.Equal(t, "", latest.Cursor)
	})

	t.Run("MaxSizeEvictionMarksTruncated", func(t *testing.T) {
		if maxSize <= 0 {
			t.Skip("unbounded store; eviction not testable")
		}
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src10"})
		// Append maxSize + 5 events. The first 5 must be evicted.
		total := maxSize + 5
		for i := 1; i <= total; i++ {
			_, _ = store.Append(ctx, AppendEventRequest{
				SourceName: "src10", Event: ev(strconv.Itoa(i), "e"+strconv.Itoa(i)),
			})
		}
		// Poll asking for cursor "1" — that event is evicted; expect Truncated=true.
		resp, _ := store.Poll(ctx, PollEventsRequest{SourceName: "src10", Cursor: "1", Limit: 100})
		assert.True(t, resp.Truncated, "polling for an evicted cursor must surface Truncated=true")
	})

	t.Run("ConcurrentAppendsAreSerialized", func(t *testing.T) {
		_, _ = store.Truncate(ctx, TruncateEventsRequest{SourceName: "src11"})
		const n = 100
		var wg sync.WaitGroup
		for i := 1; i <= n; i++ {
			wg.Add(1)
			go func(seq int) {
				defer wg.Done()
				_, _ = store.Append(ctx, AppendEventRequest{
					SourceName: "src11", Event: ev(strconv.Itoa(seq), "e"+strconv.Itoa(seq)),
				})
			}(i)
		}
		wg.Wait()
		// We don't care about order under contention (caller serializes
		// via its own lock today; the store just shouldn't crash or
		// drop events). Just check the count.
		recent, _ := store.Recent(ctx, RecentEventsRequest{SourceName: "src11", N: n + 10})
		if maxSize <= 0 || n <= maxSize {
			assert.Len(t, recent.Events, n)
		} else {
			assert.Len(t, recent.Events, maxSize)
		}
	})
}

func TestInMemoryEventBufferStore_Conformance(t *testing.T) {
	runEventBufferConformance(t, NewInMemoryEventBufferStore(50), 50)
}

func TestInMemoryEventBufferStore_Unbounded(t *testing.T) {
	runEventBufferConformance(t, NewInMemoryEventBufferStore(0), 0)
}

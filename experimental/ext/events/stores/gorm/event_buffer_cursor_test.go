package gormstore

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// TestEventBufferStore_ProvideCursors covers the issue-833 store-minted
// path: with WithProvideCursors, Append assigns a globally-monotone cursor
// from sequence_no for a nil Event.Cursor, and Poll/Latest/Truncate all
// project and filter on it. This is the multi-writer topology — N replicas
// each Append with a nil cursor, the DB sequence keeps them unique and
// ordered.
func TestEventBufferStore_ProvideCursors(t *testing.T) {
	db := openSQLite(t)
	s, err := NewEventBufferStore(db, WithProvideCursors())
	require.NoError(t, err)
	ctx := context.Background()

	cps, ok := s.(events.CursorProvidingStore)
	require.True(t, ok, "store must implement CursorProvidingStore")
	require.True(t, cps.ProvidesCursor("chat.message"))

	// N writers append with a nil cursor; the store mints each one.
	var cursors []string
	for i := 0; i < 6; i++ {
		resp, err := s.Append(ctx, events.AppendEventRequest{
			SourceName: "chat.message",
			Event:      events.Event{EventID: fmt.Sprintf("e%d", i), Timestamp: "t", Cursor: nil},
		})
		require.NoError(t, err)
		require.NotEmpty(t, resp.Cursor, "store must mint a cursor for a nil Event.Cursor")
		cursors = append(cursors, resp.Cursor)
	}

	// Unique + strictly increasing.
	seen := make(map[string]bool, len(cursors))
	var prev int64
	for i, c := range cursors {
		assert.False(t, seen[c], "duplicate minted cursor: %s", c)
		seen[c] = true
		n, err := strconv.ParseInt(c, 10, 64)
		require.NoError(t, err)
		if i > 0 {
			assert.Greater(t, n, prev)
		}
		prev = n
	}

	// Poll from the start returns all six with the minted cursors stamped.
	resp, err := s.Poll(ctx, events.PollEventsRequest{SourceName: "chat.message", Cursor: "", Limit: 100})
	require.NoError(t, err)
	require.Len(t, resp.Events, 6)
	for i, e := range resp.Events {
		require.NotNil(t, e.Cursor, "polled event must carry the projected cursor")
		assert.Equal(t, cursors[i], *e.Cursor)
	}

	// Gap-free resume: from cursors[2] we get exactly the strictly-later tail.
	tail, err := s.Poll(ctx, events.PollEventsRequest{SourceName: "chat.message", Cursor: cursors[2], Limit: 100})
	require.NoError(t, err)
	assert.Len(t, tail.Events, 3)

	// Latest projects the last minted cursor.
	lat, err := s.Latest(ctx, events.LatestCursorRequest{SourceName: "chat.message"})
	require.NoError(t, err)
	assert.Equal(t, cursors[5], lat.Cursor)

	// Truncate filters on the effective (sequence_no) cursor: <= cursors[1]
	// drops the first two.
	rm, err := s.Truncate(ctx, events.TruncateEventsRequest{SourceName: "chat.message", BeforeCursor: cursors[1]})
	require.NoError(t, err)
	assert.Equal(t, 2, rm.Removed)
}

// TestEventBufferStore_DefaultDoesNotProvideCursors pins the default: without
// WithProvideCursors the store advertises no cursor capability and stores the
// caller's cursor verbatim (a nil cursor yields an empty AppendEventResponse).
func TestEventBufferStore_DefaultDoesNotProvideCursors(t *testing.T) {
	db := openSQLite(t)
	s, err := NewEventBufferStore(db)
	require.NoError(t, err)
	ctx := context.Background()

	cps, ok := s.(events.CursorProvidingStore)
	require.True(t, ok, "store always implements the method")
	assert.False(t, cps.ProvidesCursor("x"), "default store must not provide cursors")

	resp, err := s.Append(ctx, events.AppendEventRequest{
		SourceName: "x",
		Event:      events.Event{EventID: "e", Timestamp: "t", Cursor: nil},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Cursor, "verbatim store must not mint a cursor")
}

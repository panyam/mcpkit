package events

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for WithEventBufferStore wiring — confirms YieldingSource
// honors the option end-to-end: yield writes to the store, Poll
// reads from the store. Existing in-memory ring stays populated as
// the ByCursor/Recent backwards-compat path.

type testData struct {
	Msg string `json:"msg"`
}

func TestYieldingSource_WithEventBufferStore_DualWrite(t *testing.T) {
	store := NewInMemoryEventBufferStore(100)
	src, yield := NewYieldingSource[testData](
		EventDef{Name: "test.dual-write"},
		WithEventBufferStore(store),
	)
	ctx := t.Context()
	require.NoError(t, yield(ctx, testData{Msg: "hello"}))
	require.NoError(t, yield(ctx, testData{Msg: "world"}))

	// Store side — events landed.
	resp, err := store.Poll(ctx, PollEventsRequest{
		SourceName: "test.dual-write", Cursor: "0", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, 2)

	// YieldingSource Poll — also returns both (delegates to store).
	pr := src.Poll("0", 10)
	require.Len(t, pr.Events, 2)
	assert.Equal(t, "test.dual-write", pr.Events[0].Name)
	assert.Equal(t, "test.dual-write", pr.Events[1].Name)
}

func TestYieldingSource_WithEventBufferStore_PollReadsStore(t *testing.T) {
	// The store and the source's in-memory ring would normally agree.
	// Pre-populate the store BEFORE the source ever yields, then Poll
	// the source — if Poll delegates to the store, we see the
	// pre-existing event.
	store := NewInMemoryEventBufferStore(100)
	ctx := context.Background()

	c := "42"
	_, err := store.Append(ctx, AppendEventRequest{
		SourceName: "test.read-from-store",
		Event: Event{
			EventID:   "preexisting",
			Name:      "test.read-from-store",
			Timestamp: "2026-06-09T00:00:00Z",
			Cursor:    &c,
			Data:      []byte(`{"msg":"from-store"}`),
		},
	})
	require.NoError(t, err)

	src, _ := NewYieldingSource[testData](
		EventDef{Name: "test.read-from-store"},
		WithEventBufferStore(store),
	)

	pr := src.Poll("0", 10)
	require.Len(t, pr.Events, 1)
	assert.Equal(t, "preexisting", pr.Events[0].EventID,
		"Poll must read from the store, not from the source's empty in-memory ring")
	assert.Equal(t, "42", pr.Cursor)
}

func TestYieldingSource_WithoutEventBufferStore_DefaultStillWorks(t *testing.T) {
	// No store wired — existing single-replica behavior unchanged.
	src, yield := NewYieldingSource[testData](EventDef{Name: "test.no-store"})
	ctx := t.Context()
	require.NoError(t, yield(ctx, testData{Msg: "x"}))
	require.NoError(t, yield(ctx, testData{Msg: "y"}))

	pr := src.Poll("0", 10)
	assert.Len(t, pr.Events, 2, "default ring still in play when no store is wired")
}

func TestYieldingSource_WithEventBufferStore_TruncatedFlag(t *testing.T) {
	// Eviction via maxSize=3 on the store; yield 5 events; Poll for
	// cursor=1 → store reports Truncated=true.
	store := NewInMemoryEventBufferStore(3)
	src, yield := NewYieldingSource[testData](
		EventDef{Name: "test.truncated"},
		WithEventBufferStore(store),
	)
	ctx := t.Context()
	for i := 0; i < 5; i++ {
		require.NoError(t, yield(ctx, testData{Msg: "n"}))
	}
	pr := src.Poll("1", 10)
	assert.True(t, pr.Truncated, "Poll for an evicted cursor must surface Truncated=true via the store path")
}

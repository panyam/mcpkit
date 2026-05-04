package events

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePayload struct {
	Msg string `json:"msg"`
}

// TestYieldingSource_Implements verifies the constructor returns a value that
// satisfies the EventSource interface — the library treats yielding sources
// as ordinary EventSources for events/list and events/poll.
func TestYieldingSource_Implements(t *testing.T) {
	src, _ := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	var _ EventSource = src
	assert.Equal(t, "fake", src.Def().Name)
}

// TestYieldingSource_AutoDerivesPayloadSchema verifies PayloadSchema is filled
// in from the type parameter when the caller didn't supply one — same
// ergonomic as TypedSource so the two paths stay symmetric.
func TestYieldingSource_AutoDerivesPayloadSchema(t *testing.T) {
	src, _ := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	assert.NotNil(t, src.Def().PayloadSchema, "schema should be auto-derived from generic param")
}

// TestYieldingSource_YieldAppearsOnPoll verifies the round-trip: yield(data)
// stores the event so a subsequent Poll surfaces it. Core spec contract:
// push-style production with pull-style consumption.
func TestYieldingSource_YieldAppearsOnPoll(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "hello"}))
	require.NoError(t, yield(fakePayload{Msg: "world"}))

	pr := src.Poll("", 10)
	require.Len(t, pr.Events, 2)
	assert.Equal(t, "fake", pr.Events[0].Name)
	assert.NotEmpty(t, pr.Events[0].Cursor)
	assert.NotEmpty(t, pr.Events[0].EventID)
	assert.NotEqual(t, pr.Events[0].EventID, pr.Events[1].EventID, "event IDs must be unique")

	var got fakePayload
	require.NoError(t, json.Unmarshal(pr.Events[0].Data, &got))
	assert.Equal(t, "hello", got.Msg)
}

// TestYieldingSource_PollHonorsCursor verifies polling with the cursor of the
// last seen event returns only events appended after it — strict-after
// semantics. Without this clients would receive duplicates on every poll.
func TestYieldingSource_PollHonorsCursor(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "a"}))
	c1 := src.Poll("", 10).Events[0].CursorStr()

	require.NoError(t, yield(fakePayload{Msg: "b"}))
	require.NoError(t, yield(fakePayload{Msg: "c"}))

	pr := src.Poll(c1, 10)
	require.Len(t, pr.Events, 2)
}

// TestYieldingSource_PollRespectsLimit verifies the limit caps the returned
// slice. The library's events/poll handler relies on this to detect HasMore
// by requesting limit+1 and trimming.
func TestYieldingSource_PollRespectsLimit(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	for i := 0; i < 5; i++ {
		require.NoError(t, yield(fakePayload{Msg: "x"}))
	}
	pr := src.Poll("", 2)
	assert.Len(t, pr.Events, 2)
}

// TestYieldingSource_EvictionAndTruncated verifies the bounded ring evicts
// oldest events past WithMaxSize, and Poll reports Truncated=true when the
// requested cursor predates the oldest surviving event. Matches the spec's
// truncated signal — clients SHOULD treat it as a possible gap.
func TestYieldingSource_EvictionAndTruncated(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"}, WithMaxSize(3))
	require.NoError(t, yield(fakePayload{Msg: "1"}))
	c1 := src.Poll("", 10).Events[0].CursorStr()
	require.NoError(t, yield(fakePayload{Msg: "2"}))
	require.NoError(t, yield(fakePayload{Msg: "3"}))
	require.NoError(t, yield(fakePayload{Msg: "4"})) // evicts c1
	require.NoError(t, yield(fakePayload{Msg: "5"})) // evicts second

	assert.Equal(t, 3, src.Len(), "buffer stays bounded at WithMaxSize")

	pr := src.Poll(c1, 100)
	assert.True(t, pr.Truncated, "truncated should be true when requested cursor was evicted")
	assert.Len(t, pr.Events, 3, "remaining events still returned")
}

// TestYieldingSource_PollReportsLatestCursorWhenNoNewEvents verifies a poll
// at the head returns no events but advances the client's cursor to the head.
// Without this the client's cursor would stay stale and they would keep
// re-polling the same tail.
func TestYieldingSource_PollReportsLatestCursorWhenNoNewEvents(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "a"}))
	first := src.Poll("", 10)
	headCursor := first.Cursor

	pr := src.Poll(headCursor, 10)
	assert.Empty(t, pr.Events)
	assert.Equal(t, headCursor, pr.Cursor, "cursor stays at head when no new events")
}

// TestYieldingSource_FanoutHookFiresOncePerYield verifies the library wires
// SetEmitHook so each yield triggers exactly one downstream emit. This is
// what Register uses to push events to SSE subscribers and webhook
// deliverers without the source author writing fanout code.
func TestYieldingSource_FanoutHookFiresOncePerYield(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})

	var fired int32
	var mu sync.Mutex
	var seen []Event

	src.SetEmitHook(func(e Event) {
		atomic.AddInt32(&fired, 1)
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	})

	require.NoError(t, yield(fakePayload{Msg: "one"}))
	require.NoError(t, yield(fakePayload{Msg: "two"}))
	require.NoError(t, yield(fakePayload{Msg: "three"}))

	assert.Equal(t, int32(3), atomic.LoadInt32(&fired))
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 3)
	assert.NotEqual(t, seen[0].Cursor, seen[1].Cursor, "fanout receives the cursor-stamped event")
}

// TestYieldingSource_NoFanoutBeforeRegister verifies yield works without an
// emit hook installed — useful for tests, and required so the source can be
// constructed before Register wires fanout. Events still land in the buffer.
func TestYieldingSource_NoFanoutBeforeRegister(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "early"}))
	pr := src.Poll("", 10)
	assert.Len(t, pr.Events, 1)
}

// TestYieldingSource_RecentReturnsTypedTail verifies the typed accessor
// returns the last n payloads as their original Data type, with no
// unmarshal cycle. This is the path resource handlers use to avoid
// re-decoding the wire format.
func TestYieldingSource_RecentReturnsTypedTail(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	for _, msg := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, yield(fakePayload{Msg: msg}))
	}

	got := src.Recent(3)
	require.Len(t, got, 3)
	assert.Equal(t, "c", got[0].Msg, "Recent returns oldest-first within the tail window")
	assert.Equal(t, "d", got[1].Msg)
	assert.Equal(t, "e", got[2].Msg)
}

// TestYieldingSource_RecentClampsToBufferSize verifies asking for more than
// the buffer holds returns everything available rather than panicking.
func TestYieldingSource_RecentClampsToBufferSize(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "only"}))

	got := src.Recent(50)
	assert.Len(t, got, 1)
}

// TestYieldingSource_RecentEmptyOnZero verifies n<=0 returns nil rather than
// the entire buffer — guards against accidental "give me everything" calls.
func TestYieldingSource_RecentEmptyOnZero(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "x"}))
	assert.Nil(t, src.Recent(0))
	assert.Nil(t, src.Recent(-1))
}

// TestYieldingSource_ByCursorFindsTypedPayload verifies the per-cursor lookup
// returns the typed payload directly. Resource templates that serve
// per-event URIs use this.
func TestYieldingSource_ByCursorFindsTypedPayload(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "first"}))
	require.NoError(t, yield(fakePayload{Msg: "second"}))

	first := src.Poll("", 10).Events[0]
	got, ok := src.ByCursor(first.CursorStr())
	assert.True(t, ok)
	assert.Equal(t, "first", got.Msg)
}

// TestYieldingSource_ByCursorMissingReturnsZero verifies an unknown cursor
// returns (zero, false). Caller must check ok before using the value.
func TestYieldingSource_ByCursorMissingReturnsZero(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	require.NoError(t, yield(fakePayload{Msg: "x"}))

	got, ok := src.ByCursor("nonexistent-cursor")
	assert.False(t, ok)
	assert.Empty(t, got.Msg, "missing cursor returns zero value")
}

// TestYieldingSource_DefaultsMaxSize verifies WithMaxSize(0) and absence of
// the option both fall back to the default. Protects against accidental
// zero-cap buffers that would silently drop every event.
func TestYieldingSource_DefaultsMaxSize(t *testing.T) {
	s1, _ := NewYieldingSource[fakePayload](EventDef{Name: "x"})
	assert.Equal(t, defaultYieldingMaxSize, s1.maxSize)

	s2, _ := NewYieldingSource[fakePayload](EventDef{Name: "x"}, WithMaxSize(0))
	assert.Equal(t, defaultYieldingMaxSize, s2.maxSize, "WithMaxSize(0) keeps default")

	s3, _ := NewYieldingSource[fakePayload](EventDef{Name: "x"}, WithMaxSize(-5))
	assert.Equal(t, defaultYieldingMaxSize, s3.maxSize, "WithMaxSize(-5) keeps default")
}

// TestYieldingSource_ConcurrentYields verifies concurrent yield() calls are
// safe and all events land in the buffer. Exercises the source under
// contention.
func TestYieldingSource_ConcurrentYields(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = yield(fakePayload{Msg: "x"})
		}()
	}
	wg.Wait()

	assert.Equal(t, n, src.Len())
	pr := src.Poll("", 1000)
	assert.Len(t, pr.Events, n)
}

// TestYieldingSource_MetaFunc verifies the per-event metadata mapper:
// when SetMetaFunc is installed, yielded events carry Event.Meta set
// from the mapper output. Pins the spec follow-on `_meta` plumbing
// at the source-construction layer (vs the wire layer covered by
// wire_shape_test.go).
func TestYieldingSource_MetaFunc(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetMetaFunc(func(d fakePayload) map[string]any {
		return map[string]any{"length": len(d.Msg)}
	})
	require.NoError(t, yield(fakePayload{Msg: "hello"}))

	pr := src.Poll("", 10)
	require.Len(t, pr.Events, 1)
	require.NotNil(t, pr.Events[0].Meta, "metaFunc returning a non-empty map must populate Event.Meta")
	assert.Equal(t, 5, pr.Events[0].Meta["length"])
}

// TestYieldingSource_MetaFuncReturningNilOmits verifies an empty/nil
// map from the mapper produces no `_meta` on the wire (omitempty).
// Catches over-eager population that would emit `_meta: {}` for every
// event, defeating the bytes-on-wire-free common case.
func TestYieldingSource_MetaFuncReturningNilOmits(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetMetaFunc(func(d fakePayload) map[string]any { return nil })
	require.NoError(t, yield(fakePayload{Msg: "x"}))

	pr := src.Poll("", 10)
	require.Len(t, pr.Events, 1)
	assert.Nil(t, pr.Events[0].Meta, "nil/empty mapper output must leave Event.Meta unset")

	raw, err := json.Marshal(pr.Events[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"_meta"`, "omitempty must keep _meta off the wire when nil")
}

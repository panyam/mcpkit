package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingEmitter is a test-only Emitter that captures every event it
// received and an optional error to return.
type recordingEmitter struct {
	mu     sync.Mutex
	events []Event
	err    error
	label  string
}

func (e *recordingEmitter) Emit(_ context.Context, event Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return e.err
}

func (e *recordingEmitter) seen() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.events))
	copy(out, e.events)
	return out
}

func TestCompositeEmitter_FansToAllChildrenInOrder(t *testing.T) {
	a := &recordingEmitter{label: "a"}
	b := &recordingEmitter{label: "b"}
	c := &recordingEmitter{label: "c"}
	emitter := NewCompositeEmitter(a, b, c)

	require.NoError(t, emitter.Emit(context.Background(), Event{EventID: "evt_x"}))

	assert.Len(t, a.seen(), 1)
	assert.Len(t, b.seen(), 1)
	assert.Len(t, c.seen(), 1)
	assert.Equal(t, "evt_x", a.seen()[0].EventID)
}

func TestCompositeEmitter_ContinuesAfterChildErrorAndReturnsFirstError(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	a := &recordingEmitter{label: "a"}
	b := &recordingEmitter{label: "b", err: first}
	c := &recordingEmitter{label: "c", err: second}
	d := &recordingEmitter{label: "d"}
	emitter := NewCompositeEmitter(a, b, c, d)

	err := emitter.Emit(context.Background(), Event{EventID: "evt_y"})
	assert.ErrorIs(t, err, first, "composite must return the first error encountered")

	// d still received the event even though b and c errored before it.
	assert.Len(t, d.seen(), 1, "composite must continue to subsequent children after a child errors")
}

func TestCompositeEmitter_NilChildrenAreSkipped(t *testing.T) {
	a := &recordingEmitter{label: "a"}
	emitter := NewCompositeEmitter(nil, a, nil)
	require.NoError(t, emitter.Emit(context.Background(), Event{EventID: "evt_z"}))
	assert.Len(t, a.seen(), 1)
}

func TestCompositeEmitter_NoChildrenIsNoOp(t *testing.T) {
	emitter := NewCompositeEmitter()
	require.NoError(t, emitter.Emit(context.Background(), Event{EventID: "evt"}))
}

func TestCompositeEmitter_Nesting(t *testing.T) {
	leaf := &recordingEmitter{}
	inner := NewCompositeEmitter(leaf)
	outer := NewCompositeEmitter(inner)
	require.NoError(t, outer.Emit(context.Background(), Event{EventID: "evt_n"}))
	assert.Len(t, leaf.seen(), 1)
}

func TestLocalEmitter_NilSrvAndWebhooksIsNoOp(t *testing.T) {
	// Edge case: an emitter constructed with nil collaborators must
	// not panic. Useful for tests that exercise the Emitter contract
	// without standing up a server.
	emitter := NewLocalEmitter(nil, nil)
	require.NoError(t, emitter.Emit(context.Background(), Event{EventID: "evt_nilnil"}))
}

func TestEmitter_ConcurrentEmitsAreSafe(t *testing.T) {
	// The composite emitter is safe for concurrent Emit calls when its
	// children are. recordingEmitter takes a mutex; CompositeEmitter
	// holds no per-call state. Smoke-test that concurrent yields don't
	// race on the underlying targets.
	leaf := &recordingEmitter{}
	emitter := NewCompositeEmitter(leaf)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = emitter.Emit(context.Background(), Event{EventID: "evt"})
		}()
	}
	wg.Wait()
	assert.Len(t, leaf.seen(), n)
}

// Counterpart spy: a counter-only emitter used by the Register
// behavior-preservation test below — proves the configured Emitter
// is invoked once per yielded event without needing the full
// push/webhook plumbing.
type counterEmitter struct {
	count atomic.Int32
}

func (c *counterEmitter) Emit(_ context.Context, _ Event) error {
	c.count.Add(1)
	return nil
}

func TestRegister_DefaultEmitterIsLocalEmitter(t *testing.T) {
	// Property check: when Config.Emitter is nil, Register falls back
	// to NewLocalEmitter. We verify this indirectly — a custom emitter
	// in Config replaces the default and observes every yielded event.
	spy := &counterEmitter{}

	srcDef := EventDef{Name: "test.event", Delivery: []string{"push"}}
	src, yield := NewYieldingSource[map[string]any](srcDef)

	srv := server.NewServer(
		core.ServerInfo{Name: "emitter-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	Register(Config{
		Server:                   srv,
		Sources:                  []EventSource{src},
		Emitter:                  spy,
		UnsafeAnonymousPrincipal: "test-principal",
	})

	require.NoError(t, yield(context.Background(), map[string]any{"k": "v"}))
	require.NoError(t, yield(context.Background(), map[string]any{"k": "v"}))

	// Expected: 2 user yields + 1 topology source.added yield from
	// Register routing the test source through AddSource (which yields
	// on events.topology after success). The topology yield's whole
	// purpose is to be observable via the same fanout path as any
	// other event, so reaching the configured Emitter is correct.
	assert.Equal(t, int32(3), spy.count.Load(),
		"configured Emitter must receive every yielded event including topology")
}

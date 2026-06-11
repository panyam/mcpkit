package events

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeRegistry returns a fresh Registry against a bare server.Server,
// matching what Register would build minus the dispatcher install.
// Useful for testing Registry mutation semantics in isolation.
func makeRegistry(t *testing.T) *Registry {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "registry-test", Version: "0.1.0"})
	return newRegistry(srv, nil, NewLocalEmitter(srv, nil), nil)
}

func TestRegistry_TopologyMetaSourcePreregistered(t *testing.T) {
	r := makeRegistry(t)
	_, ok := r.Source(TopologySourceName)
	require.True(t, ok, "newRegistry must self-register %q", TopologySourceName)
	names := r.SourceNames()
	require.Contains(t, names, TopologySourceName)
	assert.Len(t, names, 1, "newRegistry self-registers exactly the topology source")
}

func TestRegistry_AddSource_AppearsInSnapshot(t *testing.T) {
	r := makeRegistry(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{Name: "x.event"})
	require.NoError(t, r.AddSource(src))

	got, ok := r.Source("x.event")
	require.True(t, ok)
	assert.Equal(t, src, got)
	assert.Contains(t, r.SourceNames(), "x.event")
}

func TestRegistry_AddSource_DuplicateNameErrors(t *testing.T) {
	r := makeRegistry(t)
	a, _ := NewYieldingSource[map[string]any](EventDef{Name: "dup.event"})
	b, _ := NewYieldingSource[map[string]any](EventDef{Name: "dup.event"})
	require.NoError(t, r.AddSource(a))
	err := r.AddSource(b)
	assert.ErrorContains(t, err, "already registered")
}

func TestRegistry_AddSource_NilSourceErrors(t *testing.T) {
	r := makeRegistry(t)
	err := r.AddSource(nil)
	assert.ErrorContains(t, err, "nil source")
}

func TestRegistry_AddSource_EmptyNameErrors(t *testing.T) {
	r := makeRegistry(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{Name: ""})
	err := r.AddSource(src)
	assert.ErrorContains(t, err, "empty")
}

func TestRegistry_AddSource_ReservedPrefixErrors(t *testing.T) {
	r := makeRegistry(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{Name: "events.user-source"})
	err := r.AddSource(src)
	assert.ErrorContains(t, err, "reserved")
}

func TestRegistry_RemoveSource_DropsFromMap(t *testing.T) {
	r := makeRegistry(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{Name: "removable.event"})
	require.NoError(t, r.AddSource(src))
	require.NoError(t, r.RemoveSource("removable.event"))
	_, ok := r.Source("removable.event")
	assert.False(t, ok, "RemoveSource must drop from registry map")
	assert.NotContains(t, r.SourceNames(), "removable.event")
}

func TestRegistry_RemoveSource_UnknownErrors(t *testing.T) {
	r := makeRegistry(t)
	err := r.RemoveSource("nothing-here")
	assert.ErrorContains(t, err, "not registered")
}

func TestRegistry_RemoveSource_RejectsTopologyMeta(t *testing.T) {
	// SDK-internal source must not be removable via the public API —
	// the topology stream is part of the Registry's contract.
	r := makeRegistry(t)
	err := r.RemoveSource(TopologySourceName)
	assert.ErrorContains(t, err, "reserved")
	_, ok := r.Source(TopologySourceName)
	assert.True(t, ok, "topology meta-source must remain after the rejected removal")
}

// topologyRecorder captures TopologyEvent payloads as they're emitted
// through the registry's emitter for assertion in tests.
type topologyRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *topologyRecorder) Emit(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *topologyRecorder) topology() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, 0, len(r.events))
	for _, e := range r.events {
		if e.Name == TopologySourceName {
			out = append(out, e)
		}
	}
	return out
}

func TestRegistry_TopologyEvents_FireOnAddAndRemove(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "topology-test", Version: "0.1.0"})
	rec := &topologyRecorder{}
	r := newRegistry(srv, nil, rec, nil)

	src, _ := NewYieldingSource[map[string]any](EventDef{Name: "watch.event"})
	require.NoError(t, r.AddSource(src))
	require.NoError(t, r.RemoveSource("watch.event"))

	tEvs := rec.topology()
	require.Len(t, tEvs, 2, "expected one source.added + one source.removed topology event")

	// Topology events serialize the TopologyEvent payload as
	// json.RawMessage on Event.Data; decode to confirm shape.
	var ev1, ev2 TopologyEvent
	require.NoError(t, json.Unmarshal(tEvs[0].Data, &ev1))
	require.NoError(t, json.Unmarshal(tEvs[1].Data, &ev2))

	assert.Equal(t, TopologyEventTypeAdded, ev1.Type)
	assert.Equal(t, "watch.event", ev1.Name)
	assert.NotEmpty(t, ev1.Timestamp)

	assert.Equal(t, TopologyEventTypeRemoved, ev2.Type)
	assert.Equal(t, "watch.event", ev2.Name)
}

func TestRegistry_ConcurrentAddRemoveSafe(t *testing.T) {
	// Smoke test: many goroutines adding + removing distinct sources
	// concurrently. Verifies the lock placement is sound; doesn't
	// assert specific topology ordering (that's per-goroutine
	// scheduling).
	r := makeRegistry(t)
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	var added, removed atomic.Int32
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := "conc.event." + strconv.Itoa(i)
			src, _ := NewYieldingSource[map[string]any](EventDef{Name: name})
			if err := r.AddSource(src); err == nil {
				added.Add(1)
			}
			if err := r.RemoveSource(name); err == nil {
				removed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(N), added.Load(), "all distinct AddSources should succeed")
	assert.Equal(t, int32(N), removed.Load(), "all RemoveSources should succeed")
	// Only the topology meta-source remains.
	assert.Equal(t, []string{TopologySourceName}, r.SourceNames())
}

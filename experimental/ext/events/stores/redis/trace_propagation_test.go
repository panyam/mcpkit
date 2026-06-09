package redisstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// W3C example traceparent — valid per the spec (version 00, all-hex,
// non-zero trace-id + parent-id). Used as the publisher-side input
// and asserted on the subscriber side.
const (
	testTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01"
	testTracestate  = "vendor=truth"
)

// TestEmit_InjectsTraceContextFromContext verifies the load-bearing
// publisher behavior: when ctx carries a TraceContext (set by the
// server trace middleware in real deployments), Emit stamps the
// W3C bare-name keys onto event.Meta before encoding so the
// subscriber side can stitch.
func TestEmit_InjectsTraceContextFromContext(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)

	ctx := mcpcore.WithTraceContext(t.Context(), mcpcore.TraceContext{
		Traceparent: testTraceparent,
		Tracestate:  testTracestate,
	})
	require.NoError(t, pub.Emit(ctx, sample("chat.message", "with-trace")))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond)

	got := cap.snapshot()[0]
	assert.Equal(t, testTraceparent, got.Meta[mcpcore.MetaKeyTraceparent])
	assert.Equal(t, testTracestate, got.Meta[mcpcore.MetaKeyTracestate])
}

// TestEmit_PreservesCallerSetTraceparent locks the precedence rule:
// when the caller has already stamped `_meta.traceparent` explicitly
// on the event, the publisher MUST NOT clobber it with the ctx's
// value. Mirrors core.InjectTraceContextIntoParams's "caller-set
// wins" semantics — important for systems that bridge in a
// different trace context (e.g., a webhook receiver replaying an
// upstream traceparent it parsed from headers).
func TestEmit_PreservesCallerSetTraceparent(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)

	const callerTP = "00-aabbccddeeff00112233445566778899-1122334455667788-01"
	ctx := mcpcore.WithTraceContext(t.Context(), mcpcore.TraceContext{
		Traceparent: testTraceparent, // would lose
	})
	ev := sample("chat.message", "caller-set")
	ev.Meta = map[string]any{mcpcore.MetaKeyTraceparent: callerTP}
	require.NoError(t, pub.Emit(ctx, ev))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond)

	got := cap.snapshot()[0]
	assert.Equal(t, callerTP, got.Meta[mcpcore.MetaKeyTraceparent],
		"caller-set traceparent must win over ctx-derived value")
}

// TestEmit_NoCtxTraceContext_LeavesMetaClean verifies that when ctx
// carries no TraceContext, Emit does NOT pollute event.Meta with
// empty keys or a derived blank map. Important for the common
// single-replica path where tracing isn't wired — operators
// shouldn't see a phantom `_meta.traceparent` appear on every wire
// message.
func TestEmit_NoCtxTraceContext_LeavesMetaClean(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)

	require.NoError(t, pub.Emit(t.Context(), sample("chat.message", "no-trace")))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond)

	got := cap.snapshot()[0]
	_, hasTP := got.Meta[mcpcore.MetaKeyTraceparent]
	assert.False(t, hasTP, "no ctx trace context: traceparent must NOT appear in Meta")
	_, hasTS := got.Meta[mcpcore.MetaKeyTracestate]
	assert.False(t, hasTS, "no ctx trace context: tracestate must NOT appear in Meta")
}

// TestRoundTrip_TraceContextStitchesAcrossPubsub is the end-to-end
// contract: publisher emits with a TraceContext in ctx; subscriber's
// deliverFn receives a ctx that carries the SAME TraceContext via
// core.TraceContextFromContext. That's what makes the publisher's
// span the parent of the deliverFn's span in Tempo / Jaeger.
func TestRoundTrip_TraceContextStitchesAcrossPubsub(t *testing.T) {
	opts := Options{Client: newTestClient(t)}

	var (
		mu          sync.Mutex
		deliveredTC mcpcore.TraceContext
		hits        int
	)
	deliver := func(ctx context.Context, _ events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		deliveredTC = mcpcore.TraceContextFromContext(ctx)
		hits++
		return nil
	}
	sub, err := NewSubscriber(opts, deliver)
	require.NoError(t, err)
	require.NoError(t, sub.Subscribe(t.Context(), "chat.message"))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = sub.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		_ = sub.Close()
		<-done
	}()
	time.Sleep(50 * time.Millisecond)

	pub, err := NewPublisher(opts)
	require.NoError(t, err)
	pubCtx := mcpcore.WithTraceContext(t.Context(), mcpcore.TraceContext{
		Traceparent: testTraceparent,
		Tracestate:  testTracestate,
	})
	require.NoError(t, pub.Emit(pubCtx, sample("chat.message", "stitched")))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits == 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, testTraceparent, deliveredTC.Traceparent,
		"deliverFn ctx must carry the publisher's traceparent across the Redis hop")
	assert.Equal(t, testTracestate, deliveredTC.Tracestate)
}

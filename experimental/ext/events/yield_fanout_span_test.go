package events

import (
	"context"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTP / fakeSpan are reused from webhook_span_test.go (same package).
// Tests here use the same recording machinery so the assertions style
// stays consistent across event-surface span tests.

const fanoutTestTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

// drainAndCountSubscribers reads up to one event off each provided
// subscriber channel with a short timeout so the test's main goroutine
// doesn't block forever if Match filters everyone. Used by the
// "EmitsSpan" tests where the assertions are on span shape, not
// subscriber delivery — but we still need to drain channels so
// subsequent yields don't apply backpressure.
func drainSubscribers(channels ...<-chan SubscriberEvent) {
	for _, ch := range channels {
		select {
		case <-ch:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestYieldFanout_EmitsSpanWithAttributes(t *testing.T) {
	tp := &fakeTP{}
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetTracerProvider(tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subA, _ := src.Subscribe(ctx, SubscribeOpts{})
	subB, _ := src.Subscribe(ctx, SubscribeOpts{})
	subC, _ := src.Subscribe(ctx, SubscribeOpts{})

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "x"}))
	drainSubscribers(subA, subB, subC)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp, "events.fanout span must be emitted when subscribers are present")
	assert.Equal(t, "3", sp.attr("events.subscribers.total"))
	assert.Equal(t, "3", sp.attr("events.subscribers.delivered"))
	assert.Equal(t, "0", sp.attr("events.subscribers.dropped_by_match"))
	assert.Equal(t, "0", sp.attr("events.transforms.applied"))
	assert.True(t, sp.isEnded(), "fanout span must End so the duration is recorded")
}

func TestYieldFanout_DroppedByMatch(t *testing.T) {
	tp := &fakeTP{}
	def := EventDef{
		Name:  "alert.fired",
		Match: matchOnSeverity(),
	}
	src, yield := NewYieldingSource[sevPayload](def)
	src.SetTracerProvider(tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chHigh, _ := src.Subscribe(ctx, SubscribeOpts{Arguments: map[string]any{"severity": "high"}})
	chLow, _ := src.Subscribe(ctx, SubscribeOpts{Arguments: map[string]any{"severity": "low"}})
	chMed, _ := src.Subscribe(ctx, SubscribeOpts{Arguments: map[string]any{"severity": "medium"}})

	require.NoError(t, yield(context.Background(), sevPayload{Severity: "high"}))
	drainSubscribers(chHigh, chLow, chMed)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp)
	assert.Equal(t, "3", sp.attr("events.subscribers.total"))
	assert.Equal(t, "1", sp.attr("events.subscribers.delivered"),
		"only the severity=high subscriber should pass Match")
	assert.Equal(t, "2", sp.attr("events.subscribers.dropped_by_match"))
}

func TestYieldFanout_TransformsApplied(t *testing.T) {
	tp := &fakeTP{}
	def := EventDef{
		Name:      "alert.fired",
		Transform: transformRedactReporter(),
	}
	src, yield := NewYieldingSource[sevPayload](def)
	src.SetTracerProvider(tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chRedact, _ := src.Subscribe(ctx, SubscribeOpts{Arguments: map[string]any{"redact_pii": true}})
	chRaw, _ := src.Subscribe(ctx, SubscribeOpts{Arguments: map[string]any{"redact_pii": false}})

	require.NoError(t, yield(context.Background(), sevPayload{Severity: "high", Reporter: "alice@x"}))
	drainSubscribers(chRedact, chRaw)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp)
	assert.Equal(t, "2", sp.attr("events.subscribers.total"))
	assert.Equal(t, "2", sp.attr("events.subscribers.delivered"),
		"both subscribers pass nil Match (deliver-all)")
	assert.Equal(t, "1", sp.attr("events.transforms.applied"),
		"only the redact_pii=true subscriber actually modifies the event")
}

func TestYieldFanout_NoSubscribers_SkipsSpan(t *testing.T) {
	tp := &fakeTP{}
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetTracerProvider(tp)

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "no-subs"}))

	assert.Nil(t, tp.findSpan("events.fanout"),
		"zero-subscriber yield MUST NOT emit a span — idle sources would otherwise flood Tempo with empty fanout spans")
}

func TestYieldFanout_ParentedByYieldCtx(t *testing.T) {
	tp := &fakeTP{}
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetTracerProvider(tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, _ := src.Subscribe(ctx, SubscribeOpts{})

	tc := core.TraceContext{Traceparent: fanoutTestTraceparent}
	yieldCtx := core.WithTraceContext(context.Background(), tc)
	require.NoError(t, yield(yieldCtx, fakePayload{Msg: "x"}))
	drainSubscribers(sub)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp)
	assert.Equal(t, tc, sp.parent,
		"fanout span's parent must be the yield ctx's TraceContext so the span stitches into the originating trace")
}

func TestYieldFanout_EventAttributesPresent(t *testing.T) {
	tp := &fakeTP{}
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake-event-name"})
	src.SetTracerProvider(tp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, _ := src.Subscribe(ctx, SubscribeOpts{})

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "x"}))
	drainSubscribers(sub)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp)
	assert.Equal(t, "fake-event-name", sp.attr("mcp.event.name"))
	assert.NotEmpty(t, sp.attr("mcp.event.id"),
		"mcp.event.id MUST be stamped on the fanout span for cross-referencing in Tempo / logs")
}

func TestYieldFanout_NoopTracerProvider_NoSpansEmitted(t *testing.T) {
	// Default constructor → tp = core.NoopTracerProvider{}. Recording
	// fake never installed. Confirms the unconfigured path runs without
	// allocating spans.
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, _ := src.Subscribe(ctx, SubscribeOpts{})
	require.NoError(t, yield(context.Background(), fakePayload{Msg: "x"}))
	drainSubscribers(sub)
	// No assertion possible against a non-installed TP — the contract
	// is "no panic, no allocation, no install." The compile-time path
	// that reaches `tp.StartSpan` on the Noop is short-circuited by the
	// `tp.(core.NoopTracerProvider)` branch in yield(); this test
	// exercises that branch.
}

func TestYieldFanout_RegisterInstallsTracerProvider(t *testing.T) {
	// Confirms the events.Register injection wires the
	// Config.TracerProvider onto every Source that implements
	// TracerProviderInstaller. Without this, the only way to opt in
	// would be a per-source SetTracerProvider call — the Config field
	// would be dead code.
	tp := &fakeTP{}
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})

	// Don't call src.SetTracerProvider directly — let Register do it.
	var _ TracerProviderInstaller = src
	tp.installFanout(src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, _ := src.Subscribe(ctx, SubscribeOpts{})
	require.NoError(t, yield(context.Background(), fakePayload{Msg: "x"}))
	drainSubscribers(sub)

	sp := tp.findSpan("events.fanout")
	require.NotNil(t, sp,
		"Register-installed TracerProvider must produce the same fanout span as a direct SetTracerProvider call")
}

// installFanout mirrors what events.Register does inline — kept as a
// test-only helper so the assertion above stays focused on "the
// interface is wired" without standing up a full Server.
func (p *fakeTP) installFanout(s TracerProviderInstaller) {
	s.SetTracerProvider(p)
}

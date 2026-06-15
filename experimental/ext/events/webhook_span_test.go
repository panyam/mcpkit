package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSpan + fakeTracerProvider — local stub matching server/trace_middleware_test.go
// shape so the webhook tests can assert span emission without a real
// OTel SDK (experimental/ext/events stays dep-free of ext/otel).
type fakeSpan struct {
	name   string
	parent core.TraceContext
	attrs  map[string]string
	errors []error
	ended  bool
	mu     sync.Mutex
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}
func (s *fakeSpan) SetAttribute(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]string{}
	}
	s.attrs[k] = v
}
func (s *fakeSpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
}
func (s *fakeSpan) AddLink(core.Link) {}

func (s *fakeSpan) attr(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[k]
}
func (s *fakeSpan) errCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.errors)
}
func (s *fakeSpan) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

type fakeTP struct {
	mu    sync.Mutex
	spans []*fakeSpan
}

func (p *fakeTP) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &fakeSpan{
		name:   name,
		parent: core.TraceContextFromContext(ctx),
		attrs:  make(map[string]string, len(attrs)),
	}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.mu.Lock()
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	return core.WithActiveSpan(ctx, sp), sp
}

func (p *fakeTP) snapshot() []*fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*fakeSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

func (p *fakeTP) findSpan(name string) *fakeSpan {
	for _, sp := range p.snapshot() {
		if sp.name == name {
			return sp
		}
	}
	return nil
}

// WebhookRegistry with WithWebhookTracerProvider emits an
// events.webhook.deliver span around each outbound POST. Span parent
// matches the inbound trace context resolved from ctx, attributes
// describe the delivery, and the span ends after the retry loop
// terminates (success or exhaustion).
func TestWebhookDeliver_EmitsSpanWithAttributes(t *testing.T) {
	var hits int
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	tp := &fakeTP{}
	wh := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookTracerProvider(tp),
	)
	registerTestTarget(t, wh, receiver.URL)

	ctx := ctxWithTraceparent(demoTraceparent)
	event := MakeEvent("fake.event", "evt_span_1", "1", time.Now(), map[string]string{"k": "v"})
	wh.Deliver(ctx, event)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return hits >= 1 },
		3*time.Second, 20*time.Millisecond)

	// Spans are emitted in a goroutine, so give them a moment to land.
	require.Eventually(t, func() bool { return tp.findSpan("events.webhook.deliver") != nil },
		3*time.Second, 20*time.Millisecond, "events.webhook.deliver span must emit")

	span := tp.findSpan("events.webhook.deliver")
	require.NotNil(t, span)
	require.Eventually(t, func() bool { return span.isEnded() },
		3*time.Second, 20*time.Millisecond, "span must end after retry loop terminates")

	assert.Equal(t, demoTraceparent, span.parent.Traceparent,
		"inbound trace context must become the span's parent (Gate 3 stitching)")
	assert.NotEmpty(t, span.attr("webhook.target.id"))
	assert.Equal(t, receiver.URL, span.attr("webhook.url"))
	assert.Equal(t, "fake.event", span.attr("mcp.event.name"))
	assert.Equal(t, "POST", span.attr("http.method"))
	assert.Equal(t, "200", span.attr("http.response.status_code"))
	assert.Equal(t, "1", span.attr("webhook.retry.attempts"))
	assert.Equal(t, 0, span.errCount(), "success path must not record any error")
}

// 5xx retry-exhausted path → span carries the final status code + the
// total attempts count + a RecordError reflecting the categorical bucket.
func TestWebhookDeliver_RetriesExhausted_RecordsError(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	tp := &fakeTP{}
	wh := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookTracerProvider(tp),
	)
	registerTestTarget(t, wh, receiver.URL)

	wh.Deliver(context.Background(),
		MakeEvent("fake.event", "evt_500", "1", time.Now(), map[string]string{"k": "v"}))

	// Retry timing: 0.5s + 1s + 2s = ~3.5s total; wait a bit longer for end.
	require.Eventually(t, func() bool {
		sp := tp.findSpan("events.webhook.deliver")
		return sp != nil && sp.isEnded()
	}, 6*time.Second, 50*time.Millisecond, "span must end after retries exhausted")

	span := tp.findSpan("events.webhook.deliver")
	require.NotNil(t, span)
	assert.Equal(t, "500", span.attr("http.response.status_code"),
		"final attempt's status code lands on the span")
	assert.Equal(t, "4", span.attr("webhook.retry.attempts"),
		"4 total attempts (1 initial + 3 retries)")
	assert.GreaterOrEqual(t, span.errCount(), 1,
		"final retry-exhausted path must RecordError with categorical bucket")
}

// Nil / default TracerProvider must skip span emission cleanly —
// zero overhead on the unconfigured path. Matches the
// JWTValidator / AppHost SEP-414 P6 precedent.
func TestWebhookDeliver_NoTracerProvider_NoSpan(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	registerTestTarget(t, wh, receiver.URL)

	// Drive a delivery; the Noop StartSpan returns a no-op Span; should
	// not panic and the goroutine should exit cleanly.
	wh.Deliver(context.Background(),
		MakeEvent("fake.event", "evt_noop", "1", time.Now(), map[string]string{"k": "v"}))

	time.Sleep(200 * time.Millisecond)
	// No assertion to make beyond "didn't panic" — the value of the
	// test is exercising the unconfigured path under the same
	// signatures as the instrumented path.
}

// registerTestTarget — same minimal helper used by trace_propagation_test.go,
// inlined here so this file can run standalone.
func registerTestTarget(t *testing.T, wh *WebhookRegistry, receiverURL string) {
	t.Helper()
	target := WebhookTarget{
		EventName: "fake.event",
		URL:       receiverURL,
		Secret:    generateSecret(),
		ExpiresAt: ptrTime(time.Now().Add(time.Hour)),
		Status:    DeliveryStatus{Active: true},
	}
	target.CanonicalKey = canonicalKey("test-principal", receiverURL, "fake.event", nil)
	target.ID = deriveSubscriptionID(target.CanonicalKey)
	_, err := wh.store.SaveWebhook(context.Background(), SaveWebhookRequest{Target: target})
	require.NoError(t, err)
}

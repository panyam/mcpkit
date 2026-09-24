package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A source's gap and terminal signals reach its webhook subscribers, not
// only its push streams (#1466). Spec §"Gaps and truncated", webhook row:
// a gap detected between refreshes is POSTed as {type:gap, cursor:<fresh>};
// §"Event Type Removal": subscriptions are ended with each mode's
// termination signal, which for webhooks is a terminated envelope.

type envelopeSink struct {
	mu   sync.Mutex
	got  map[string][]map[string]any // path → control envelopes, in arrival order
	srv  *httptest.Server
	hits atomic.Int32
}

func newEnvelopeSink(t *testing.T) *envelopeSink {
	t.Helper()
	s := &envelopeSink{got: map[string][]map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if _, ok := m["type"]; ok {
				s.mu.Lock()
				s.got[r.URL.Path] = append(s.got[r.URL.Path], m)
				s.mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *envelopeSink) on(path, typ string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, m := range s.got[path] {
		if m["type"] == typ {
			out = append(out, m)
		}
	}
	return out
}

func subscribeWebhook(webhooks *WebhookRegistry, sink *envelopeSink, event, path string) {
	webhooks.Register(RegisterParams{
		CanonicalKey: []byte(event + path),
		DerivedID:    "sub_" + strings.Trim(path, "/"),
		URL:          sink.srv.URL + path,
		Secret:       generateSecret(),
		EventName:    event,
		Principal:    "test-principal",
	})
}

type signalPayload struct {
	N int `json:"n"`
}

func signalSource(name string, opts ...YieldingOption) (*YieldingSource[signalPayload], func(context.Context, signalPayload) error) {
	return NewYieldingSource[signalPayload](EventDef{Name: name, Delivery: []string{"poll", "push", "webhook"}}, opts...)
}

func TestYieldGap_PostsGapToWebhookSubscribersOfThatTypeOnly(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	src, yield := signalSource("gappy.event")
	other, _ := signalSource("other.event")
	require.NoError(t, reg.AddSource(src))
	require.NoError(t, reg.AddSource(other))

	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "gappy.event", "/a")
	subscribeWebhook(webhooks, sink, "gappy.event", "/b")
	subscribeWebhook(webhooks, sink, "other.event", "/c")

	require.NoError(t, yield(t.Context(), signalPayload{1}))
	require.NoError(t, yield(t.Context(), signalPayload{2}))
	want := src.Latest()
	require.NotEmpty(t, want)

	require.NoError(t, src.YieldGap())

	for _, path := range []string{"/a", "/b"} {
		require.Eventually(t, func() bool { return len(sink.on(path, "gap")) == 1 }, 3*time.Second, 20*time.Millisecond, path)
		assert.Equal(t, want, sink.on(path, "gap")[0]["cursor"], path)
	}
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, sink.on("/c", "gap"), "a gap on one type must not reach another type's subscribers")
	assert.Len(t, webhooks.Targets(), 3, "a gap ends nothing")
}

func TestYieldGap_SendsNoEnvelopeWithoutAPosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []YieldingOption
		seed bool
	}{
		{"empty source", nil, false},
		{"cursorless source", []YieldingOption{WithoutCursors()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, webhooks, _ := buildRemovalStack(t)
			src, yield := signalSource("quiet.event", tc.opts...)
			require.NoError(t, reg.AddSource(src))
			sink := newEnvelopeSink(t)
			subscribeWebhook(webhooks, sink, "quiet.event", "/q")
			if tc.seed {
				require.NoError(t, yield(t.Context(), signalPayload{1}))
			}
			require.Empty(t, src.Latest())

			require.NoError(t, src.YieldGap())
			time.Sleep(300 * time.Millisecond)
			assert.Empty(t, sink.on("/q", "gap"), "a gap envelope with no cursor has nothing for the client to persist")
		})
	}
}

func TestYieldTerminated_TerminatesWebhookSubscribersOfThatTypeOnly(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	src, _ := signalSource("ending.event")
	other, _ := signalSource("other.event")
	require.NoError(t, reg.AddSource(src))
	require.NoError(t, reg.AddSource(other))

	var removed atomic.Int32
	webhooks.AddOnRemoveHook(func(WebhookTarget) { removed.Add(1) })

	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "ending.event", "/a")
	subscribeWebhook(webhooks, sink, "ending.event", "/b")
	subscribeWebhook(webhooks, sink, "other.event", "/c")

	require.NoError(t, src.YieldTerminated(EventDeliveryError{
		Code: ErrCodeForbidden, Message: "upstream revoked", Data: map[string]any{"reason": "rotated"},
	}))

	for _, path := range []string{"/a", "/b"} {
		require.Eventually(t, func() bool { return len(sink.on(path, "terminated")) == 1 }, 3*time.Second, 20*time.Millisecond, path)
		errObj, _ := sink.on(path, "terminated")[0]["error"].(map[string]any)
		assert.EqualValues(t, ErrCodeForbidden, errObj["code"], path)
		assert.Equal(t, "upstream revoked", errObj["message"], path)
		assert.Equal(t, map[string]any{"reason": "rotated"}, errObj["data"], path)
	}
	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, "other.event", targets[0].EventName)
	assert.EqualValues(t, 2, removed.Load(), "onRemove fires once per ended subscription, which is what releases quota")
}

func TestRemoveSource_StillSendsOneTerminatedPerSubscription(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	src, _ := signalSource("doomed.event")
	require.NoError(t, reg.AddSource(src))
	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "doomed.event", "/d")

	require.NoError(t, reg.RemoveSource("doomed.event"))

	require.Eventually(t, func() bool { return len(sink.on("/d", "terminated")) >= 1 }, 3*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, sink.on("/d", "terminated"), 1, "removal terminates webhooks itself, then YieldTerminated finds none left")
}

func TestPostGap_EmptyCursorSendsNothing(t *testing.T) {
	webhooks := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "any.event", "/e")

	webhooks.PostGap([]byte("any.event/e"), "")
	assert.Equal(t, 0, webhooks.PostGapByEventName("any.event", ""))
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, sink.hits.Load())
}

func TestPostGapByEventName_CountsWhatItSignalled(t *testing.T) {
	webhooks := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "any.event", "/x")
	subscribeWebhook(webhooks, sink, "any.event", "/y")
	subscribeWebhook(webhooks, sink, "else.event", "/z")

	assert.Equal(t, 2, webhooks.PostGapByEventName("any.event", "42"))
	assert.Equal(t, 0, webhooks.PostGapByEventName("", "42"))
	require.Eventually(t, func() bool {
		return len(sink.on("/x", "gap")) == 1 && len(sink.on("/y", "gap")) == 1
	}, 3*time.Second, 20*time.Millisecond)
	assert.Empty(t, sink.on("/z", "gap"))
}

func TestSourceSignals_RaceWithYields(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	src, yield := signalSource("busy.event")
	require.NoError(t, reg.AddSource(src))
	sink := newEnvelopeSink(t)
	subscribeWebhook(webhooks, sink, "busy.event", "/r")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = yield(context.Background(), signalPayload{i}) }()
		go func() { defer wg.Done(); _ = src.YieldGap() }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = src.YieldTerminated(EventDeliveryError{Code: ErrCodeNotFound, Message: "done"})
	}()
	wg.Wait()
	require.Eventually(t, func() bool { return len(webhooks.Targets()) == 0 }, 3*time.Second, 20*time.Millisecond)
}

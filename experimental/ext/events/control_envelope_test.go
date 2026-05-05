package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ζ-4 — control envelopes (type:gap, type:terminated).
//
// Spec §"Non-event webhook bodies" L415-423: the server POSTs signed
// envelopes to webhook receivers when (a) a gap is detected between
// refreshes (type:gap, carries fresh cursor) or (b) the subscription
// has ended (type:terminated, carries error). Both use Standard
// Webhooks headers + X-MCP-Subscription-Id, distinguished from event
// deliveries by the top-level `type` field discriminator AND by the
// webhook-id pattern: msg_<type>_<random> vs eventId for events
// (see α's newMessageID; ζ-4 finally uses the typed form).

// captureControlPost records what the receiver saw — body, headers —
// for the test to inspect after PostGap / PostTerminated returns.
type captureControlPost struct {
	mu      sync.Mutex
	body    []byte
	headers http.Header
	hits    int
}

func (c *captureControlPost) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.body = body
		c.headers = r.Header.Clone()
		c.hits++
		w.WriteHeader(http.StatusOK)
	}
}

func readAll(r interface {
	Read(p []byte) (n int, err error)
}) ([]byte, error) {
	const max = 64 * 1024
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > max {
				return buf, nil
			}
		}
		if err != nil {
			return buf, nil
		}
	}
}

// TestControlEnvelope_GapShape verifies PostGap POSTs a body matching
// {type:"gap", cursor:"<fresh>"} per spec L415, with Standard Webhooks
// headers (webhook-id, webhook-timestamp, webhook-signature) and the
// γ-4 X-MCP-Subscription-Id header. webhook-id format is
// msg_gap_<random> per spec, distinguishing control POSTs from event
// deliveries (which use eventId).
func TestControlEnvelope_GapShape(t *testing.T) {
	cap := &captureControlPost{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	canonical := canonicalKey("alice", srv.URL, "fake.event", nil)
	subID := deriveSubscriptionID(canonical)
	r.Register(canonical, subID, srv.URL, "whsec_"+strings.Repeat("a", 32), 0)

	r.PostGap(canonical, "fresh-cursor-123")

	require.Eventually(t, func() bool {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		return cap.hits == 1
	}, 2*time.Second, 20*time.Millisecond, "expected exactly one gap POST")

	cap.mu.Lock()
	defer cap.mu.Unlock()

	// Body shape.
	var env map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &env))
	assert.Equal(t, "gap", env["type"], "envelope type MUST be 'gap'; got %v", env["type"])
	assert.Equal(t, "fresh-cursor-123", env["cursor"],
		"gap envelope MUST carry the fresh cursor for the receiver to resume from")

	// Headers — Standard Webhooks.
	wid := cap.headers.Get("webhook-id")
	assert.True(t, strings.HasPrefix(wid, "msg_gap_"),
		"webhook-id MUST be msg_<type>_<random> per spec L417; got %q", wid)
	assert.NotEmpty(t, cap.headers.Get("webhook-timestamp"))
	assert.NotEmpty(t, cap.headers.Get("webhook-signature"))

	// γ-4 X-MCP-Subscription-Id MUST appear on every webhook delivery,
	// including control envelopes.
	assert.Equal(t, subID, cap.headers.Get("X-MCP-Subscription-Id"),
		"control envelopes MUST carry X-MCP-Subscription-Id (γ-4 + spec L390/L472)")
}

// TestControlEnvelope_TerminatedShape verifies PostTerminated POSTs
// {type:"terminated", error:{code, message}} per spec L420. webhook-id
// is msg_terminated_<random>. After a successful POST the registry
// removes the target so subsequent event yields don't try to deliver
// to a subscription the receiver already knows is dead.
func TestControlEnvelope_TerminatedShape(t *testing.T) {
	cap := &captureControlPost{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	canonical := canonicalKey("alice", srv.URL, "fake.event", nil)
	subID := deriveSubscriptionID(canonical)
	r.Register(canonical, subID, srv.URL, "whsec_"+strings.Repeat("a", 32), 0)

	r.PostTerminated(canonical, ControlError{Code: -32012, Message: "Unauthorized"})

	require.Eventually(t, func() bool {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		return cap.hits == 1
	}, 2*time.Second, 20*time.Millisecond)

	cap.mu.Lock()
	defer cap.mu.Unlock()

	var env map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &env))
	assert.Equal(t, "terminated", env["type"])
	errMap, ok := env["error"].(map[string]any)
	require.True(t, ok, "terminated envelope MUST include an error object; got %v", env["error"])
	assert.EqualValues(t, -32012, errMap["code"])
	assert.Equal(t, "Unauthorized", errMap["message"])

	wid := cap.headers.Get("webhook-id")
	assert.True(t, strings.HasPrefix(wid, "msg_terminated_"),
		"webhook-id MUST be msg_terminated_<random>; got %q", wid)
	assert.Equal(t, subID, cap.headers.Get("X-MCP-Subscription-Id"))

	// After PostTerminated, the registry MUST drop the target so the
	// next yield doesn't try to deliver to it.
	assert.Empty(t, r.Targets(),
		"PostTerminated MUST remove the target from the registry")
}

// TestControlEnvelope_TypeDiscriminatorIsTopLevel verifies the spec
// contract that lets receivers route by inspecting the body without
// per-payload heuristics: the discriminator is a top-level `type`
// field, not nested inside `data` or anywhere else. Receivers
// implementing the spec wire shape can branch once on this field.
func TestControlEnvelope_TypeDiscriminatorIsTopLevel(t *testing.T) {
	cap := &captureControlPost{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	canonical := canonicalKey("alice", srv.URL, "fake.event", nil)
	subID := deriveSubscriptionID(canonical)
	r.Register(canonical, subID, srv.URL, "whsec_"+strings.Repeat("a", 32), 0)

	r.PostGap(canonical, "c1")

	require.Eventually(t, func() bool {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		return cap.hits == 1
	}, time.Second, 20*time.Millisecond)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	body := string(cap.body)
	// Sanity: `type` appears at the JSON-object root, not inside another
	// object. Cheap textual check sufficient since marshaling is stable.
	assert.True(t, strings.HasPrefix(body, `{"type":`),
		"top-level field MUST be `type`; got body: %s", body)
}

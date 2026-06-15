package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the ttlMs + refreshBefore:null no-expiry shape (issue 760,
// spec PR1 commits 99f3589c + 6967f2ba). Cover the wire-shape matrix
// the spec lays out: absent, in-envelope, below floor (clamp UP),
// above ceiling (clamp DOWN), null without opt-in (finite default),
// null with opt-in (refreshBefore: null). Plus prune-loop exemption
// for no-expiry targets and canonical-key stability across ttlMs
// variants.

const ttlTestReceiverURL = "https://example.test/wh"

func subscribeWithTTL(t *testing.T, srv *server.Server, ttlMsJSON string) map[string]any {
	t.Helper()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// Build the body manually so we can splice an arbitrary `ttlMs`
	// JSON literal (number, null, or absent) without going through Go's
	// *int64 collapse.
	body := `{"name":"fake.event","delivery":{"mode":"webhook","url":"` + receiver.URL +
		`","secret":"` + generateSecret() + `"}`
	if ttlMsJSON != "" {
		body += `,"ttlMs":` + ttlMsJSON
	}
	body += `}`

	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe",
		Params: json.RawMessage(body),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "subscribe failed: %+v", resp.Error)

	// resp.Result is the map[string]any the handler returned.
	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TestSubscribe_TTLMs_Absent_GrantsServerDefault — omitting ttlMs means
// "server picks." mcpkit's default is 1h (DefaultWebhookTTL); the
// granted refreshBefore should be ~1h in the future.
func TestSubscribe_TTLMs_Absent_GrantsServerDefault(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	result := subscribeWithTTL(t, srvAdapter(srv), "")

	rb, ok := result["refreshBefore"].(string)
	require.True(t, ok, "refreshBefore should be a string for finite grant; got %T", result["refreshBefore"])

	parsed, err := time.Parse(time.RFC3339, rb)
	require.NoError(t, err)
	delta := time.Until(parsed)
	assert.InDelta(t, DefaultWebhookTTL.Seconds(), delta.Seconds(), 5,
		"absent ttlMs should produce a grant near DefaultWebhookTTL (~1h); got delta=%v", delta)
}

// TestSubscribe_TTLMs_InEnvelope_GrantedAsSuggested — a suggestion that
// falls cleanly inside [MinWebhookTTL, MaxWebhookTTL] flows through
// unmodified. spec PR1 commit 99f3589c: "SHOULD be ≤ the suggestion."
// mcpkit honours the suggestion verbatim when in-envelope (= the
// strongest possible ≤).
func TestSubscribe_TTLMs_InEnvelope_GrantedAsSuggested(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	suggestion := 10 * time.Minute
	result := subscribeWithTTL(t, srvAdapter(srv), formatMs(suggestion))

	rb, ok := result["refreshBefore"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, rb)
	require.NoError(t, err)
	delta := time.Until(parsed)
	assert.InDelta(t, suggestion.Seconds(), delta.Seconds(), 5,
		"in-envelope suggestion should be granted ~verbatim; got delta=%v", delta)
}

// TestSubscribe_TTLMs_BelowFloor_ClampedUpToMinimum — the spec's "one
// sanctioned exception" to SHOULD-be-≤: a server MAY clamp UP to its
// minimum TTL to defend against refresh storms. mcpkit clamps to
// MinWebhookTTL (5 minutes by default).
func TestSubscribe_TTLMs_BelowFloor_ClampedUpToMinimum(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	// Suggest 10 seconds — well below the 5-minute floor.
	result := subscribeWithTTL(t, srvAdapter(srv), formatMs(10*time.Second))

	rb, ok := result["refreshBefore"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, rb)
	require.NoError(t, err)
	delta := time.Until(parsed)
	assert.InDelta(t, MinWebhookTTL.Seconds(), delta.Seconds(), 5,
		"sub-floor suggestion should be clamped UP to MinWebhookTTL; got delta=%v", delta)
}

// TestSubscribe_TTLMs_AboveCeiling_ClampedDownToMaximum — a 1-year
// suggestion exceeds MaxWebhookTTL (24h); mcpkit clamps DOWN. Spec
// PR1 commit 99f3589c: "SHOULD be ≤ the suggestion." Clamping down
// honours the inequality straightforwardly.
func TestSubscribe_TTLMs_AboveCeiling_ClampedDownToMaximum(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	// Suggest 1 year (well above the 24-hour ceiling).
	result := subscribeWithTTL(t, srvAdapter(srv), formatMs(365*24*time.Hour))

	rb, ok := result["refreshBefore"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, rb)
	require.NoError(t, err)
	delta := time.Until(parsed)
	assert.InDelta(t, MaxWebhookTTL.Seconds(), delta.Seconds(), 5,
		"super-ceiling suggestion should be clamped DOWN to MaxWebhookTTL; got delta=%v", delta)
}

// TestSubscribe_TTLMs_Null_NotOptedIn_GrantsFiniteDefault — the spec's
// "A server unwilling to grant it simply returns a finite refreshBefore,
// which the client treats like any other grant" path. Without
// WithAllowInfiniteWebhookTTL, `ttlMs: null` collapses to the server
// default. No rejection.
func TestSubscribe_TTLMs_Null_NotOptedIn_GrantsFiniteDefault(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	result := subscribeWithTTL(t, srvAdapter(srv), "null")

	rb, ok := result["refreshBefore"].(string)
	require.True(t, ok, "without opt-in, refreshBefore MUST be a finite RFC3339 string; got %T", result["refreshBefore"])
	parsed, err := time.Parse(time.RFC3339, rb)
	require.NoError(t, err)
	delta := time.Until(parsed)
	assert.InDelta(t, DefaultWebhookTTL.Seconds(), delta.Seconds(), 5,
		"null without opt-in should fall through to DefaultWebhookTTL; got delta=%v", delta)
}

// TestSubscribe_TTLMs_Null_OptedIn_GrantsRefreshBeforeNull — operator
// opted into no-expiry via WithAllowInfiniteWebhookTTL. ttlMs:null
// gets the explicit no-expiry grant: refreshBefore serialises as JSON
// null.
func TestSubscribe_TTLMs_Null_OptedIn_GrantsRefreshBeforeNull(t *testing.T) {
	srv, _ := buildAuthGateStackWithOpts(t, "test-principal", WithAllowInfiniteWebhookTTL())
	result := subscribeWithTTL(t, srvAdapter(srv), "null")

	rb, has := result["refreshBefore"]
	require.True(t, has, "refreshBefore MUST be present even on no-expiry; got %+v", result)
	assert.Nil(t, rb, "with opt-in, ttlMs:null MUST emit refreshBefore: null; got %T = %v", rb, rb)
}

// TestRefreshBefore_WireShape_NullSerialisesAsJSONNull — independent
// of the handler, the response shape must actually serialise refreshBefore
// as JSON null, not as the string "null" or as omitted. Marshal the
// handler's response via json.Marshal and grep the bytes.
func TestRefreshBefore_WireShape_NullSerialisesAsJSONNull(t *testing.T) {
	srv, _ := buildAuthGateStackWithOpts(t, "test-principal", WithAllowInfiniteWebhookTTL())

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	body := `{"name":"fake.event","ttlMs":null,"delivery":{"mode":"webhook","url":"` + receiver.URL +
		`","secret":"` + generateSecret() + `"}}`
	resp, err := srvAdapter(srv).Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe",
		Params: json.RawMessage(body),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	wireBytes, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(wireBytes), `"refreshBefore":null`,
		"no-expiry grant MUST serialise refreshBefore as JSON null; got %s", string(wireBytes))
}

// TestPruneLoop_SkipsNoExpiryTargets — the prune loop is the GC for
// finite-TTL subscriptions. Per spec PR1 commit 99f3589c: "TTL expiry
// no longer garbage-collects orphans" for no-expiry subs. Test:
// register a no-expiry target with a stale ExpiresAt sentinel (nil),
// fire pruneExpiredLocked, assert the target survives.
func TestPruneLoop_SkipsNoExpiryTargets(t *testing.T) {
	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithAllowInfiniteWebhookTTL())

	// Register one no-expiry + one finite-but-expired target.
	noExpiryKey := canonicalKey("alice", "https://no.expiry.test/wh", "fake.event", nil)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: noExpiryKey, DerivedID: "sub_noexpiry", URL: "https://no.expiry.test/wh",
		Secret: "whsec_test", EventName: "fake.event", Principal: "alice",
		NoExpiry: true,
	})
	expiredKey := canonicalKey("alice", "https://finite.test/wh", "fake.event", nil)
	past := time.Now().Add(-1 * time.Hour)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: expiredKey, DerivedID: "sub_expired", URL: "https://finite.test/wh",
		Secret: "whsec_test", EventName: "fake.event", Principal: "alice",
		ExpiresAtOverride: &past,
	})

	// pruneExpiredLocked is private; the simplest way to fire it is to
	// register again, which is documented as "Side effect: prunes
	// expired targets before registering."
	probeKey := canonicalKey("alice", "https://probe.test/wh", "fake.event", nil)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: probeKey, DerivedID: "sub_probe", URL: "https://probe.test/wh",
		Secret: "whsec_test", EventName: "fake.event", Principal: "alice",
	})

	_, foundNoExpiry := r.lookupTarget(noExpiryKey)
	_, foundExpired := r.lookupTarget(expiredKey)
	assert.True(t, foundNoExpiry, "no-expiry target MUST survive prune loop")
	assert.False(t, foundExpired, "expired finite target MUST be pruned")
}

// TestCanonicalKey_IndependentOfTTLMs — two subscribes with different
// ttlMs values on the same (principal, name, arguments, url) hit the
// SAME canonical key, so the registry treats the second as an
// idempotent refresh. The TTL only changes the resulting ExpiresAt,
// not the identity.
func TestCanonicalKey_IndependentOfTTLMs(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	secret := generateSecret()
	subscribe := func(ttlMsJSON string) string {
		body := `{"name":"fake.event","delivery":{"mode":"webhook","url":"` + receiver.URL +
			`","secret":"` + secret + `"}`
		if ttlMsJSON != "" {
			body += `,"ttlMs":` + ttlMsJSON
		}
		body += `}`
		resp, err := srvAdapter(srv).Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe",
			Params: json.RawMessage(body),
		})
		require.NoError(t, err)
		require.Nil(t, resp.Error)
		raw, _ := json.Marshal(resp.Result)
		var r struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(raw, &r))
		return r.ID
	}

	id1 := subscribe(formatMs(10 * time.Minute))
	id2 := subscribe(formatMs(1 * time.Hour))
	id3 := subscribe("")
	assert.Equal(t, id1, id2, "same (principal, name, url) with different ttlMs MUST hit same canonical id")
	assert.Equal(t, id1, id3, "absent ttlMs MUST also resolve to the same canonical id")
}

// TestNegotiateExpiry_DirectMatrix exercises the helper directly so
// regressions surface without spinning up the dispatch path. Cheap
// table-driven coverage of the absent/null/numeric branches.
func TestNegotiateExpiry_DirectMatrix(t *testing.T) {
	type want struct {
		noExpiry bool
		// expectedDuration is roughly time.Until(expiresAt); 0 means
		// "expect nil" (no-expiry case).
		expectedDuration time.Duration
		// tolerance is how much the test allows for the time between
		// the negotiation call and the assertion.
		tolerance time.Duration
	}
	cases := []struct {
		name        string
		raw         string
		allowInfTTL bool
		want        want
	}{
		{"absent", "", false, want{false, DefaultWebhookTTL, 2 * time.Second}},
		{"null without opt-in", "null", false, want{false, DefaultWebhookTTL, 2 * time.Second}},
		{"null with opt-in", "null", true, want{true, 0, 0}},
		{"in-envelope number", formatMs(10 * time.Minute), false, want{false, 10 * time.Minute, 2 * time.Second}},
		{"below floor", "10000", false, want{false, MinWebhookTTL, 2 * time.Second}},
		{"above ceiling", formatMs(365 * 24 * time.Hour), false, want{false, MaxWebhookTTL, 2 * time.Second}},
		{"negative ms treated as absent", "-1", false, want{false, DefaultWebhookTTL, 2 * time.Second}},
		{"garbage treated as absent", `"not a number"`, false, want{false, DefaultWebhookTTL, 2 * time.Second}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []WebhookOption{WithWebhookAllowPrivateNetworks(true)}
			if tc.allowInfTTL {
				opts = append(opts, WithAllowInfiniteWebhookTTL())
			}
			r := NewWebhookRegistry(opts...)
			noExpiry, expiresAt := r.NegotiateExpiry(json.RawMessage(tc.raw))
			assert.Equal(t, tc.want.noExpiry, noExpiry)
			if tc.want.noExpiry {
				assert.Nil(t, expiresAt)
				return
			}
			require.NotNil(t, expiresAt)
			delta := time.Until(*expiresAt)
			assert.InDelta(t, tc.want.expectedDuration.Seconds(), delta.Seconds(), tc.want.tolerance.Seconds())
		})
	}
}

// srvAdapter exists only to let earlier iterations of these tests
// pass an interface to the dispatch fixture. Final shape just passes
// *server.Server through, so this is a no-op identity that keeps the
// call sites readable.
func srvAdapter(s *server.Server) *server.Server { return s }

func formatMs(d time.Duration) string {
	return formatInt(d.Milliseconds())
}

func formatInt(n int64) string {
	// itoa without depending on strconv just for one call.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Endpoint verification, spec §"Webhook Security" → "Endpoint
// verification (anti-flooding)": a server MUST NOT deliver to a callback
// until the endpoint has confirmed it wants deliveries. Issue 490.

// recordedPost is one POST a verificationReceiver saw, in arrival order.
type recordedPost struct {
	header http.Header
	body   []byte
	json   map[string]any
}

// verificationReceiver is an httptest receiver whose answer to a
// verification envelope is pluggable, so one fixture covers the echoing,
// mis-echoing and refusing endpoints.
type verificationReceiver struct {
	*httptest.Server
	mu    sync.Mutex
	posts []recordedPost
	// answer writes the response to a verification envelope. nil echoes
	// the challenge correctly.
	answer func(w http.ResponseWriter, challenge string)
}

func newVerificationReceiver(t *testing.T, answer func(w http.ResponseWriter, challenge string)) *verificationReceiver {
	t.Helper()
	vr := &verificationReceiver{answer: answer}
	vr.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		vr.mu.Lock()
		vr.posts = append(vr.posts, recordedPost{header: r.Header.Clone(), body: body, json: m})
		vr.mu.Unlock()
		if m["type"] != "verification" {
			w.WriteHeader(http.StatusOK)
			return
		}
		challenge, _ := m["challenge"].(string)
		if vr.answer != nil {
			vr.answer(w, challenge)
			return
		}
		echoChallenge(w, challenge)
	}))
	t.Cleanup(vr.Close)
	return vr
}

func echoChallenge(w http.ResponseWriter, challenge string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
}

func (vr *verificationReceiver) snapshot() []recordedPost {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	return append([]recordedPost(nil), vr.posts...)
}

func (vr *verificationReceiver) verificationCount() int {
	n := 0
	for _, p := range vr.snapshot() {
		if p.json["type"] == "verification" {
			n++
		}
	}
	return n
}

// buildVerifyingStack is buildAuthGateStackWithOpts with endpoint
// verification left at its default, which the shared helper turns off.
func buildVerifyingStack(t *testing.T, unsafeAnon string, extra ...WebhookOption) (*server.Server, *WebhookRegistry) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	webhooks := NewWebhookRegistry(append([]WebhookOption{WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks()}, extra...)...)
	webhooks.client.Timeout = 2 * time.Second
	Register(Config{
		Sources:                  []EventSource{fakeSecretValidationSource{}},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: unsafeAnon,
	})
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: core.NewRawJSON(initParams),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	_, err = srv.Dispatch(context.Background(), &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	require.NoError(t, err)
	return srv, webhooks
}

func webhookSubscribeParams(url, secret string, args map[string]any) map[string]any {
	p := map[string]any{
		"name":     "fake.event",
		"delivery": map[string]any{"mode": "webhook", "url": url, "secret": secret},
	}
	if args != nil {
		p["arguments"] = args
	}
	return p
}

func callbackErrorReason(t *testing.T, resp *core.Response) string {
	t.Helper()
	require.NotNil(t, resp.Error, "expected -32015; got success")
	require.Equal(t, ErrCodeCallbackEndpointError, resp.Error.Code, "error: %+v", resp.Error)
	raw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var data CallbackEndpointErrorData
	require.NoError(t, json.Unmarshal(raw, &data))
	return data.Reason
}

func TestVerification_ChallengePrecedesFirstDelivery(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, webhooks := buildVerifyingStack(t, "alice")
	secret := generateSecret()

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, secret, nil))
	require.Nil(t, resp.Error, "subscribe to an echoing endpoint must succeed: %+v", resp.Error)
	result, _ := json.Marshal(resp.Result)
	var sub struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(result, &sub))

	webhooks.Deliver(context.Background(), MakeEvent("fake.event", "evt_1", "1", time.Now(), map[string]string{"k": "v"}))
	require.Eventually(t, func() bool { return len(rcv.snapshot()) >= 2 }, 2*time.Second, 20*time.Millisecond)

	posts := rcv.snapshot()
	first := posts[0]
	assert.Equal(t, "verification", first.json["type"], "the first POST must be the verification envelope; got %s", first.body)
	challenge, ok := first.json["challenge"].(string)
	assert.True(t, ok && challenge != "", "verification must carry a string challenge nonce; got %s", first.body)
	assert.Equal(t, sub.ID, first.header.Get("X-MCP-Subscription-Id"),
		"the verification POST is headed like a delivery so the receiver can pick the secret")
	assert.True(t, strings.HasPrefix(first.header.Get("webhook-id"), "msg_verification_"),
		"control envelopes use msg_<type>_<random>; got %q", first.header.Get("webhook-id"))
	assert.True(t, VerifyStandardWebhooksSignature(first.body, secret,
		first.header.Get("webhook-id"), first.header.Get("webhook-timestamp"), first.header.Get("webhook-signature")),
		"the verification POST must be signed with the subscription secret")
	assert.Equal(t, "evt_1", posts[1].json["eventId"], "the event follows the handshake")
}

func TestVerification_WrongEchoRejectsSubscribe(t *testing.T) {
	const leak = "internal-hostname-db01.corp"
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"challenge":"not-it","debug":"` + leak + `"}`))
	})
	srv, webhooks := buildVerifyingStack(t, "alice")

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil))

	assert.Equal(t, string(DeliveryErrorChallengeFailed), callbackErrorReason(t, resp))
	assert.NotContains(t, resp.Error.Message, leak, "endpoint responses must never reach the subscriber")
	assert.Empty(t, webhooks.Targets(), "a failed handshake must not leave a subscription behind")

	webhooks.Deliver(context.Background(), MakeEvent("fake.event", "evt_1", "1", time.Now(), map[string]string{"k": "v"}))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, len(rcv.snapshot()), "only the verification POST may reach an endpoint that failed it")
}

func TestVerification_Non2xxEchoIsChallengeFailed(t *testing.T) {
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, challenge string) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
	})
	srv, _ := buildVerifyingStack(t, "alice")

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil))

	assert.Equal(t, string(DeliveryErrorChallengeFailed), callbackErrorReason(t, resp),
		"the echo only counts in a 2xx body")
}

func TestVerification_UnreachableEndpointReportsConnectionCategory(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	srv, webhooks := buildVerifyingStack(t, "alice")

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(url, generateSecret(), nil))

	assert.Equal(t, string(DeliveryErrorConnectionRefused), callbackErrorReason(t, resp))
	assert.Empty(t, webhooks.Targets())
}

func TestVerification_CachedAcrossRefreshAndArguments(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice")
	secret := generateSecret()

	for _, args := range []map[string]any{nil, nil, {"channel": "a"}, {"channel": "b"}} {
		resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, secret, args))
		require.Nil(t, resp.Error, "args=%v: %+v", args, resp.Error)
	}

	assert.Equal(t, 1, rcv.verificationCount(),
		"one principal verifies a URL once; refreshes and varied arguments must not multiply POSTs at the endpoint")
}

func TestVerification_AllowlistWaivesHandshake(t *testing.T) {
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) })
	srv, webhooks := buildVerifyingStack(t, "alice", WithWebhookDeliveryAllowlist([]string{rcv.URL + "/hooks"}))

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/hooks/tenant-1", generateSecret(), nil))

	require.Nil(t, resp.Error, "an allowlisted URL needs no handshake: %+v", resp.Error)
	assert.Zero(t, rcv.verificationCount())
	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.NotNil(t, targets[0].VerifiedAt, "an allowlist hit counts as verification")
}

func TestVerification_AllowlistMatchesAtSegmentBoundary(t *testing.T) {
	r := NewWebhookRegistry(WithWebhookDeliveryAllowlist([]string{
		"https://hooks.example.com/mcp",
		"https://whole.example.com",
		"not a url",
	}))
	for url, want := range map[string]bool{
		"https://hooks.example.com/mcp":          true,
		"https://hooks.example.com/mcp/tenant-1": true,
		"https://HOOKS.example.com/mcp/x":        true,
		"https://hooks.example.com/mcpx":         false,
		"https://hooks.example.com/other":        false,
		"http://hooks.example.com/mcp":           false,
		"https://hooks.example.com:8443/mcp":     false,
		"https://whole.example.com/anything":     true,
		"https://evil.example.com/mcp":           false,
	} {
		assert.Equal(t, want, r.allowlisted(url), url)
	}
}

func TestVerification_AllowlistMissFallsBackToHandshake(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	srv, _ := buildVerifyingStack(t, "alice", WithWebhookDeliveryAllowlist([]string{"https://elsewhere.example.com"}))

	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil)).Error)
	assert.Equal(t, 1, rcv.verificationCount())
}

func TestVerification_PreVerifiedURLWaivesHandshakeForThatPrincipalOnly(t *testing.T) {
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) })
	opt := WithPreVerifiedDeliveryURL("alice", rcv.URL)

	srvAlice, _ := buildVerifyingStack(t, "alice", opt)
	require.Nil(t, dispatchSubscribe(t, srvAlice, webhookSubscribeParams(rcv.URL, generateSecret(), nil)).Error)
	assert.Zero(t, rcv.verificationCount())

	srvBob, _ := buildVerifyingStack(t, "bob", opt)
	resp := dispatchSubscribe(t, srvBob, webhookSubscribeParams(rcv.URL, generateSecret(), nil))
	assert.Equal(t, string(DeliveryErrorChallengeFailed), callbackErrorReason(t, resp),
		"alice's out-of-band verification must not cover bob")
}

func TestVerification_PreVerifierSeesPrincipalAndURL(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	var gotPrincipal, gotURL string
	pv := PreVerifierFunc(func(_ context.Context, p, u string) bool {
		gotPrincipal, gotURL = p, u
		return true
	})
	srv, _ := buildVerifyingStack(t, "alice", WithPreVerifier(pv))

	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil)).Error)
	assert.Equal(t, "alice", gotPrincipal)
	assert.Equal(t, rcv.URL, gotURL)
	assert.Zero(t, rcv.verificationCount())
}

func TestVerification_UnsafeSkipDeliversUnverified(t *testing.T) {
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) })
	srv, webhooks := buildVerifyingStack(t, "alice", WithUnsafeSkipEndpointVerification())

	require.Nil(t, dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL, generateSecret(), nil)).Error)
	assert.Zero(t, rcv.verificationCount())
	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Nil(t, targets[0].VerifiedAt, "a skipped verification must not be recorded as one")
}

func TestVerification_CacheIsPerPrincipal(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	secret := generateSecret()
	verify := func(principal string) DeliveryErrorBucket {
		return r.verifyEndpoint(context.Background(), verifyEndpointParams{
			Principal: principal, URL: rcv.URL, Secret: secret, DerivedID: "sub_x",
			CanonicalKey: canonicalKey(principal, rcv.URL, "fake.event", nil),
		})
	}

	require.Equal(t, DeliveryErrorNone, verify("alice"))
	require.Equal(t, DeliveryErrorNone, verify("alice"))
	require.Equal(t, DeliveryErrorNone, verify("bob"))

	assert.Equal(t, 2, rcv.verificationCount(), "one principal's verification never waives the challenge for another")
}

func TestVerification_ReplayedEchoFails(t *testing.T) {
	var first string
	var mu sync.Mutex
	rcv := newVerificationReceiver(t, func(w http.ResponseWriter, challenge string) {
		mu.Lock()
		if first == "" {
			first = challenge
		}
		replay := first
		mu.Unlock()
		echoChallenge(w, replay)
	})
	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	verify := func(principal string) DeliveryErrorBucket {
		return r.verifyEndpoint(context.Background(), verifyEndpointParams{
			Principal: principal, URL: rcv.URL, Secret: generateSecret(), DerivedID: "sub_x",
			CanonicalKey: canonicalKey(principal, rcv.URL, "fake.event", nil),
		})
	}

	require.Equal(t, DeliveryErrorNone, verify("alice"))
	assert.Equal(t, DeliveryErrorChallengeFailed, verify("bob"),
		"each handshake mints a fresh nonce, so an echo captured from an earlier one must not pass")
}

func TestVerification_UsesSSRFGuardedClient(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	logCap := &captureLog{}
	r := ssrfTestRegistry(false, logCap)

	bucket := r.verifyEndpoint(context.Background(), verifyEndpointParams{
		Principal: "alice", URL: rcv.URL, Secret: generateSecret(), DerivedID: "sub_x",
		CanonicalKey: canonicalKey("alice", rcv.URL, "fake.event", nil),
	})

	assert.Equal(t, DeliveryErrorConnectionRefused, bucket)
	assert.Empty(t, rcv.snapshot(), "the verification POST must not reach a loopback endpoint when private networks are blocked")
	assert.True(t, logCap.containsSSRFBlock(), "logs: %v", logCap.snapshot())
}

func TestVerification_RedirectIsNotFollowed(t *testing.T) {
	rcv := newVerificationReceiver(t, nil)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, rcv.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	srv, _ := buildVerifyingStack(t, "alice")

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(redirector.URL, generateSecret(), nil))

	assert.Equal(t, string(DeliveryErrorChallengeFailed), callbackErrorReason(t, resp))
	assert.Zero(t, rcv.verificationCount(), "a redirect could point the handshake anywhere; it must not be followed")
}

func TestVerification_PersistedTargetSurvivesRestartWithoutRehandshake(t *testing.T) {
	store := NewInMemoryWebhookStore()
	rcv := newVerificationReceiver(t, nil)
	secret := generateSecret()

	srv1, _ := buildVerifyingStack(t, "alice", WithWebhookStore(store), WithAllowInfiniteWebhookTTL())
	params := webhookSubscribeParams(rcv.URL, secret, nil)
	params["ttlMs"] = nil
	require.Nil(t, dispatchSubscribe(t, srv1, params).Error)
	require.Equal(t, 1, rcv.verificationCount())

	// A fresh registry over the same store stands in for a restart: the
	// in-memory cache is gone, only the persisted target remains.
	srv2, webhooks2 := buildVerifyingStack(t, "alice", WithWebhookStore(store), WithAllowInfiniteWebhookTTL())
	require.Nil(t, dispatchSubscribe(t, srv2, params).Error)

	assert.Equal(t, 1, rcv.verificationCount(), "the stored verification carries across the restart")
	targets := webhooks2.Targets()
	require.Len(t, targets, 1)
	assert.NotNil(t, targets[0].VerifiedAt)
}

func TestAnswerVerificationChallenge(t *testing.T) {
	rec := httptest.NewRecorder()
	assert.True(t, AnswerVerificationChallenge(rec, []byte(`{"type":"verification","challenge":"n0nce"}`)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"challenge":"n0nce"}`, rec.Body.String())

	for _, body := range []string{`{"eventId":"e1","name":"x"}`, `{"type":"gap","cursor":"c"}`, `not json`} {
		rec := httptest.NewRecorder()
		assert.False(t, AnswerVerificationChallenge(rec, []byte(body)), body)
		assert.Zero(t, rec.Body.Len(), "a non-verification body must be left for the caller to answer")
	}
}

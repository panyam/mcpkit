package events

import (
	"context"
	"crypto/tls"
	"fmt"
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

// Path (d) of spec §"Endpoint verification": the callback URL's https origin
// serves /.well-known/mcp-webhook-receiver.json listing the path prefixes that
// accept deliveries, and a covered URL is verified without a challenge.
// Issue 1436.

// wellKnownReceiver is a TLS receiver whose well-known document is pluggable
// and which refuses every challenge, so a subscribe that succeeds can only
// have been verified by the document.
type wellKnownReceiver struct {
	*httptest.Server
	docFetches atomic.Int32
	challenges atomic.Int32
	mu         sync.Mutex
	doc        http.HandlerFunc
}

func newWellKnownReceiver(t *testing.T, doc http.HandlerFunc) *wellKnownReceiver {
	t.Helper()
	w := &wellKnownReceiver{doc: doc}
	w.Server = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == WellKnownReceiverPath {
			w.docFetches.Add(1)
			w.mu.Lock()
			h := w.doc
			w.mu.Unlock()
			h(rw, r)
			return
		}
		w.challenges.Add(1)
		rw.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(w.Close)
	return w
}

func serveDoc(body, cacheControl string) http.HandlerFunc {
	return func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if cacheControl != "" {
			rw.Header().Set("Cache-Control", cacheControl)
		}
		_, _ = rw.Write([]byte(body))
	}
}

// trustTestTLS points the registry's client at the httptest CA while keeping
// its SSRF-guarded dialer and redirect policy.
func trustTestTLS(r *WebhookRegistry, ts *httptest.Server) {
	tr := r.client.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{RootCAs: ts.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
}

func buildWellKnownStack(t *testing.T, rcv *wellKnownReceiver, extra ...WebhookOption) (subscribe func(path string) error, webhooks *WebhookRegistry) {
	t.Helper()
	srv, webhooks := buildVerifyingStack(t, "alice", append([]WebhookOption{WithWellKnownReceiverDocs()}, extra...)...)
	trustTestTLS(webhooks, rcv.Server)
	return func(path string) error {
		resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+path, generateSecret(), nil))
		if resp.Error != nil {
			return fmt.Errorf("%d %s", resp.Error.Code, resp.Error.Message)
		}
		return nil
	}, webhooks
}

func TestWellKnown_CoveredPathSkipsHandshake(t *testing.T) {
	rcv := newWellKnownReceiver(t, serveDoc(`{"receivers":["/hooks/"]}`, ""))
	subscribe, webhooks := buildWellKnownStack(t, rcv)

	require.NoError(t, subscribe("/hooks/tenant-1"))

	assert.Zero(t, rcv.challenges.Load(), "a covered URL needs no challenge")
	assert.Equal(t, int32(1), rcv.docFetches.Load())
	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.NotNil(t, targets[0].VerifiedAt, "a well-known match counts as verification")
}

func TestWellKnown_UncoveredPathsFallBackToHandshake(t *testing.T) {
	rcv := newWellKnownReceiver(t, serveDoc(`{"receivers":["/hooks"]}`, ""))
	subscribe, _ := buildWellKnownStack(t, rcv)

	require.NoError(t, subscribe("/hooks"))
	require.NoError(t, subscribe("/hooks/deeper"))
	for _, path := range []string{"/hooksx", "/other"} {
		err := subscribe(path)
		require.Error(t, err, "%s is not covered, so the refused challenge decides", path)
	}
	assert.Equal(t, int32(2), rcv.challenges.Load())
}

func TestWellKnown_OffByDefault(t *testing.T) {
	rcv := newWellKnownReceiver(t, serveDoc(`{"receivers":["/"]}`, ""))
	srv, webhooks := buildVerifyingStack(t, "alice")
	trustTestTLS(webhooks, rcv.Server)

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(rcv.URL+"/hooks", generateSecret(), nil))

	require.NotNil(t, resp.Error, "without the option the document is never consulted")
	assert.Zero(t, rcv.docFetches.Load())
	assert.Equal(t, int32(1), rcv.challenges.Load())
}

func TestWellKnown_BadDocumentsFallBackToHandshake(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"missing":   http.NotFound,
		"not json":  serveDoc(`not json`, ""),
		"wrong key": serveDoc(`{"delivery_uris":["/hooks/"]}`, ""),
		// Valid JSON with trailing whitespace, so only the size cap rejects it.
		"oversized": serveDoc(`{"receivers":["/hooks/"]}`+strings.Repeat(" ", 70<<10), ""),
		// A valid document in the body of a redirect, so only the status
		// check rejects it.
		"redirect": func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Location", "/elsewhere.json")
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusFound)
			_, _ = rw.Write([]byte(`{"receivers":["/hooks/"]}`))
		},
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			rcv := newWellKnownReceiver(t, doc)
			subscribe, _ := buildWellKnownStack(t, rcv)
			require.Error(t, subscribe("/hooks/a"), "a document that does not cover the URL must not verify it")
			assert.Equal(t, int32(1), rcv.challenges.Load(), "the handshake decides instead")
		})
	}
}

func TestWellKnown_PlaintextOriginNeverCounts(t *testing.T) {
	var fetches atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == WellKnownReceiverPath {
			fetches.Add(1)
			serveDoc(`{"receivers":["/"]}`, "")(rw, r)
			return
		}
		rw.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(plain.Close)
	srv, _ := buildVerifyingStack(t, "alice", WithWellKnownReceiverDocs())

	resp := dispatchSubscribe(t, srv, webhookSubscribeParams(plain.URL+"/hooks", generateSecret(), nil))

	require.NotNil(t, resp.Error)
	assert.Zero(t, fetches.Load(), "the document proves control of an https origin; an http one is not fetched")
}

func TestWellKnown_CacheHonoursMaxAge(t *testing.T) {
	rcv := newWellKnownReceiver(t, serveDoc(`{"receivers":["/hooks/"]}`, "max-age=1"))
	subscribe, _ := buildWellKnownStack(t, rcv)

	require.NoError(t, subscribe("/hooks/a"))
	require.NoError(t, subscribe("/hooks/b"))
	assert.Equal(t, int32(1), rcv.docFetches.Load(), "the document is cached per origin")

	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, subscribe("/hooks/c"))
	assert.Equal(t, int32(2), rcv.docFetches.Load(), "past max-age it is fetched again")
}

func TestWellKnown_MissingDocumentIsCachedToo(t *testing.T) {
	rcv := newWellKnownReceiver(t, http.NotFound)
	subscribe, _ := buildWellKnownStack(t, rcv)

	_ = subscribe("/hooks/a")
	_ = subscribe("/hooks/b")

	assert.Equal(t, int32(1), rcv.docFetches.Load(), "a host without a document is not asked again on every subscribe")
}

func TestWellKnown_FetchCountsAgainstHostBudget(t *testing.T) {
	rcv := newWellKnownReceiver(t, http.NotFound)
	subscribe, _ := buildWellKnownStack(t, rcv, WithVerificationRateLimit(1, time.Minute))

	err := subscribe("/hooks/a")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many endpoint verifications",
		"the document fetch spent the host's only token, so the challenge is throttled")
	assert.Zero(t, rcv.challenges.Load())
}

func TestWellKnown_FetchUsesSSRFGuardedClient(t *testing.T) {
	rcv := newWellKnownReceiver(t, serveDoc(`{"receivers":["/"]}`, ""))
	r := ssrfTestRegistry(false, nil)
	WithWellKnownReceiverDocs()(r)
	trustTestTLS(r, rcv.Server)

	bucket := r.verifyEndpoint(context.Background(), verifyEndpointParams{
		Principal: "alice", URL: rcv.URL + "/hooks", Secret: generateSecret(), DerivedID: "sub_x",
		CanonicalKey: canonicalKey("alice", rcv.URL+"/hooks", "fake.event", nil),
	})

	assert.Equal(t, DeliveryErrorConnectionRefused, bucket)
	assert.Zero(t, rcv.docFetches.Load(), "the document fetch must not reach a loopback host when private networks are blocked")
}

func TestWellKnownReceiverHandler_RoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(WellKnownReceiverPath, WellKnownReceiverHandler("/hooks/", "/mcp/events"))
	var challenges atomic.Int32
	mux.HandleFunc("/", func(rw http.ResponseWriter, _ *http.Request) {
		challenges.Add(1)
		rw.WriteHeader(http.StatusForbidden)
	})
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	srv, webhooks := buildVerifyingStack(t, "alice", WithWellKnownReceiverDocs())
	trustTestTLS(webhooks, ts)

	for _, path := range []string{"/hooks/a", "/mcp/events"} {
		resp := dispatchSubscribe(t, srv, webhookSubscribeParams(ts.URL+path, generateSecret(), nil))
		require.Nil(t, resp.Error, "%s is published by the helper: %+v", path, resp.Error)
	}
	assert.Zero(t, challenges.Load())

	res, err := ts.Client().Get(ts.URL + WellKnownReceiverPath)
	require.NoError(t, err)
	defer res.Body.Close()
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	assert.Contains(t, res.Header.Get("Cache-Control"), "max-age=")
}

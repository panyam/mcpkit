package eventsclient_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for issue 760 — Go SDK's ttlMs wire emission and refreshBefore
// null no-expiry handling. The client-side counterpart to the
// negotiation matrix on the server (ttl_negotiation_test.go).
//
// Wire sniffing pattern: stand up an httptest.Server with a sniffer
// handler that captures the request body, then forwards via
// httputil.ReverseProxy to the real MCP test server. The SDK points
// at the sniffer URL — every request gets captured before forwarding.

// snifferStack returns a connected MCP client whose requests are
// captured into the returned holder. Optional whOpts configure the
// underlying webhook registry (e.g. WithAllowInfiniteWebhookTTL).
func snifferStack(t *testing.T, whOpts ...events.WebhookOption) (*client.Client, *atomic.Pointer[string]) {
	t.Helper()

	whOpts = append([]events.WebhookOption{events.WithWebhookAllowPrivateNetworks(true)}, whOpts...)
	webhooks := events.NewWebhookRegistry(whOpts...)
	src, _ := events.NewYieldingSource[fakePayload](events.EventDef{
		Name:        "fake.event",
		Description: "test source",
		Delivery:    []string{"push", "poll", "webhook"},
	})

	srv := server.NewServer(
		core.ServerInfo{Name: "ttl-wire-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})

	realTS := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(realTS.Close)

	target, err := url.Parse(realTS.URL)
	require.NoError(t, err)

	captured := atomic.Pointer[string]{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	sniffTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Capture subscribe bodies only; initialize/etc. would clobber.
			if strings.Contains(string(body), `"method":"events/subscribe"`) {
				s := string(body)
				captured.Store(&s)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(sniffTS.Close)

	c := client.NewClient(sniffTS.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, &captured
}

// TestClient_TTLMs_Absent_OmittedFromWire — when SubscribeOptions
// leaves both TTLMs and NoExpiry at zero, the wire body MUST NOT
// carry a ttlMs key. Servers reading absent-vs-null at the
// json.RawMessage layer rely on "absent" actually meaning "key not
// present in the JSON object."
func TestClient_TTLMs_Absent_OmittedFromWire(t *testing.T) {
	c, captured := snifferStack(t)
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
	})
	require.NoError(t, err)
	defer sub.Stop()

	wire := derefString(captured)
	require.NotEmpty(t, wire)
	assert.NotContains(t, wire, `"ttlMs"`,
		"absent TTLMs MUST NOT emit a wire key (spec PR1 commit 99f3589c); got %s", wire)
}

// TestClient_TTLMs_Finite_AppearsAsNumber — a finite TTLMs is on the
// wire as a JSON number, not a string and not omitted.
func TestClient_TTLMs_Finite_AppearsAsNumber(t *testing.T) {
	ms := int64(900_000) // 15 minutes
	c, captured := snifferStack(t)
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		TTLMs:       &ms,
	})
	require.NoError(t, err)
	defer sub.Stop()

	wire := derefString(captured)
	require.NotEmpty(t, wire)
	assert.Contains(t, wire, `"ttlMs":900000`,
		"finite TTLMs MUST serialise as a JSON number; got %s", wire)
}

// TestClient_NoExpiry_AppearsAsNull — NoExpiry=true takes precedence
// over any TTLMs value and emits an explicit JSON null. Distinguishing
// null from absent on the wire is what unlocks the no-expiry grant
// path on the server.
func TestClient_NoExpiry_AppearsAsNull(t *testing.T) {
	ms := int64(900_000)
	c, captured := snifferStack(t, events.WithAllowInfiniteWebhookTTL())
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		TTLMs:       &ms, // explicitly ignored when NoExpiry=true
		NoExpiry:    true,
	})
	require.NoError(t, err)
	defer sub.Stop()

	wire := derefString(captured)
	require.NotEmpty(t, wire)
	assert.Contains(t, wire, `"ttlMs":null`,
		"NoExpiry=true MUST emit ttlMs:null verbatim; got %s", wire)
	assert.NotContains(t, wire, `"ttlMs":900000`,
		"NoExpiry=true MUST override numeric TTLMs; got %s", wire)
}

// TestClient_NoExpiry_AcceptsRefreshBeforeNull — the SDK must decode
// `refreshBefore: null` on the response and surface it via
// Subscription.RefreshBefore() returning nil.
func TestClient_NoExpiry_AcceptsRefreshBeforeNull(t *testing.T) {
	c, _, _ := stack(t, events.WithAllowInfiniteWebhookTTL())
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		NoExpiry:    true,
	})
	require.NoError(t, err)
	defer sub.Stop()

	assert.Nil(t, sub.RefreshBefore(),
		"no-expiry grant MUST surface as RefreshBefore() == nil")
}

// TestClient_NoExpiry_FallbackWhenServerDoesntOptIn — server without
// WithAllowInfiniteWebhookTTL collapses ttlMs:null into the default
// finite TTL. The SDK transparently accepts the finite refreshBefore.
func TestClient_NoExpiry_FallbackWhenServerDoesntOptIn(t *testing.T) {
	c, _, _ := stack(t) // no WithAllowInfiniteWebhookTTL
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		NoExpiry:    true,
	})
	require.NoError(t, err)
	defer sub.Stop()

	rb := sub.RefreshBefore()
	require.NotNil(t, rb, "server without WithAllowInfiniteWebhookTTL MUST return a finite refreshBefore")
	assert.True(t, rb.After(time.Now()),
		"finite fallback grant MUST land in the future; got %v", rb)
}

func derefString(p *atomic.Pointer[string]) string {
	if v := p.Load(); v != nil {
		return *v
	}
	return ""
}

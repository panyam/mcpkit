package events

import (
	"context"
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

func TestUnsafeAllowCallbackOrigin_ValidateLiftsBothGuardsForThatOriginOnly(t *testing.T) {
	r := NewWebhookRegistry()

	require.Error(t, r.ValidateWebhookURL("http://127.0.0.1:53211/hook"), "scheme guard")
	require.Error(t, r.ValidateWebhookURL("https://127.0.0.1:53211/hook"), "loopback guard")

	got, err := r.UnsafeAllowCallbackOrigin("http://127.0.0.1:53211")
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:53211", got)

	assert.NoError(t, r.ValidateWebhookURL("http://127.0.0.1:53211/hook"))
	assert.NoError(t, r.ValidateWebhookURL("http://127.0.0.1:53211"))
	assert.Error(t, r.ValidateWebhookURL("http://127.0.0.1:53212/hook"), "other port")
	assert.Error(t, r.ValidateWebhookURL("https://127.0.0.1:53211/hook"), "other scheme is another origin")
	assert.Error(t, r.ValidateWebhookURL("http://localhost:53211/hook"), "other host")
}

func TestUnsafeAllowCallbackOrigin_DialGuardLiftedForThatHostPortOnly(t *testing.T) {
	r := NewWebhookRegistry()
	dial := r.dialContextForTest()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	_, err := dial(ctx, "tcp", addr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SSRF guard")

	_, err = r.UnsafeAllowCallbackOrigin(ts.URL)
	require.NoError(t, err)

	conn, err := dial(ctx, "tcp", addr)
	require.NoError(t, err)
	conn.Close()

	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()
	_, err = dial(ctx, "tcp", strings.TrimPrefix(other.URL, "http://"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSRF guard")
}

func TestUnsafeAllowCallbackOrigin_Normalizes(t *testing.T) {
	cases := map[string]string{
		"HTTP://LocalHost:8080":    "http://localhost:8080",
		"https://Example.com:443":  "https://example.com",
		"http://example.com:80/":   "http://example.com",
		"http://[::1]:9000":        "http://[::1]:9000",
		"https://hooks.example.io": "https://hooks.example.io",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := NewWebhookRegistry().UnsafeAllowCallbackOrigin(in)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	r := NewWebhookRegistry()
	_, err := r.UnsafeAllowCallbackOrigin("HTTP://LocalHost:80")
	require.NoError(t, err)
	assert.NoError(t, r.ValidateWebhookURL("http://localhost/hook"), "default port and case are the same origin")
}

func TestUnsafeAllowCallbackOrigin_RejectsNonOrigins(t *testing.T) {
	for _, in := range []string{
		"",
		"127.0.0.1:8080",
		"ftp://127.0.0.1:21",
		"http://",
		"http://127.0.0.1:8080/path",
		"http://127.0.0.1:8080?q=1",
		"http://127.0.0.1:8080#frag",
		"http://user:pw@127.0.0.1:8080",
	} {
		t.Run(in, func(t *testing.T) {
			r := NewWebhookRegistry()
			_, err := r.UnsafeAllowCallbackOrigin(in)
			require.Error(t, err)
			assert.Error(t, r.ValidateWebhookURL("http://127.0.0.1:8080/hook"), "a rejected origin permits nothing")
		})
	}
}

// Verification and delivery share the registry's client, so permitting the
// origin has to carry both the challenge POST and the event through a
// registry whose guards are otherwise at their defaults.
func TestUnsafeAllowCallbackOrigin_VerifiesAndDeliversThroughDefaultGuards(t *testing.T) {
	var events atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if AnswerVerificationChallenge(w, body) {
			return
		}
		events.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	r := NewWebhookRegistry()
	r.client.Timeout = 2 * time.Second
	_, err := r.UnsafeAllowCallbackOrigin(ts.URL)
	require.NoError(t, err)

	url := ts.URL + "/hook"
	require.NoError(t, r.ValidateWebhookURL(url))
	secret := GenerateSecret()
	require.Equal(t, DeliveryErrorNone, r.verifyEndpoint(context.Background(), verifyEndpointParams{
		Principal: "p", URL: url, Secret: secret, DerivedID: "sub_origin",
	}))

	r.Register(RegisterParams{CanonicalKey: []byte("k"), DerivedID: "sub_origin", URL: url, Secret: secret})
	r.Deliver(context.Background(), MakeEvent("fake.event", "evt_1", "1", time.Now(), map[string]string{"text": "hi"}))
	require.Eventually(t, func() bool { return events.Load() == 1 }, 2*time.Second, 20*time.Millisecond)
}

func TestUnsafeAllowCallbackOrigin_ConcurrentWithDials(t *testing.T) {
	r := NewWebhookRegistry()
	dial := r.dialContextForTest()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = r.UnsafeAllowCallbackOrigin("http://127.0.0.1:" + string(rune('1'+i)) + "000")
		}()
		go func() {
			defer wg.Done()
			_ = r.ValidateWebhookURL("http://127.0.0.1:1000/hook")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if c, err := dial(ctx, "tcp", "127.0.0.1:1"); err == nil {
				c.Close()
			}
		}()
	}
	wg.Wait()
}

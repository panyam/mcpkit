package events

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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

// ζ-1 — delivery-time SSRF guard.
//
// The pre-ζ webhook registry only checked the URL hostname at subscribe
// time, with a "WARNING: allowed in POC mode" log that allowed loopback
// through. This is unsafe under DNS rebinding: an attacker registers a
// public hostname, then changes its DNS to resolve to an internal IP at
// delivery time. The pre-ζ check has nothing to say about that.
//
// ζ-1 moves the check into the http.Client's Dialer.Control callback so
// it runs on EVERY connect attempt against the resolved IP — TOCTOU-free
// because the IP we inspect is the same one about to be used for the
// connect syscall. Per spec §"Webhook Security" → "SSRF prevention" L464.

// captureDeliveryFailure returns a webhook registry configured with a
// captured-failure log so tests can observe whether a delivery attempt
// was blocked at the dial layer (vs reaching the server). The blocked-by-
// SSRF log lines carry a recognizable substring tests can grep.
//
// allowPrivate controls WithWebhookAllowPrivateNetworks: false (default
// for production) means loopback / private ranges are rejected at dial
// time; true (demos and tests that want to actually deliver to httptest)
// permits them.
func ssrfTestRegistry(allowPrivate bool, captureLog *captureLog) *WebhookRegistry {
	opts := []WebhookOption{}
	if allowPrivate {
		opts = append(opts, WithWebhookAllowPrivateNetworks(true))
	}
	r := NewWebhookRegistry(opts...)
	// Shorten the timeout so test failures are fast; preserve the
	// SSRF Dialer.Control + CheckRedirect from NewWebhookRegistry.
	r.client.Timeout = 2 * time.Second
	if captureLog != nil {
		captureLog.attach(r)
	}
	return r
}

// captureLog is a tiny goroutine-safe log capture used by SSRF tests to
// observe whether the dial-time check fired. Hooks the registry's deliver
// loop via the test-only setLogf hook.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) attach(r *WebhookRegistry) {
	r.setLogfForTest(func(format string, args ...any) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lines = append(c.lines, fmt.Sprintf(format, args...))
	})
}

func (c *captureLog) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func (c *captureLog) containsSSRFBlock() bool {
	for _, l := range c.snapshot() {
		if strings.Contains(l, "SSRF") || strings.Contains(l, "blocked") {
			return true
		}
	}
	return false
}

// TestDelivery_RejectsLoopbackAtDialTime verifies the dial-time guard
// rejects 127.0.0.1 — the canonical SSRF target — when the demo escape
// hatch (WithWebhookAllowPrivateNetworks) is OFF. Pre-ζ this would have
// connected silently with only a "WARNING" log; the spec requires hard
// rejection in production.
func TestDelivery_RejectsLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logCap := &captureLog{}
	r := ssrfTestRegistry(false, logCap) // allowPrivate=false → loopback BLOCKED

	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)
	r.Deliver(MakeEvent("fake.event", "evt_1", "1", time.Now(),
		map[string]string{"text": "hi"}))

	// Give the deliver goroutine a moment to fail.
	time.Sleep(200 * time.Millisecond)

	assert.True(t, logCap.containsSSRFBlock(),
		"loopback delivery should have been blocked at dial time; logs were:\n%s",
		strings.Join(logCap.snapshot(), "\n"))
}

// TestDelivery_AllowsLoopbackWithEscape verifies the demo escape hatch:
// WithWebhookAllowPrivateNetworks(true) permits loopback dials so demos
// can deliver to local httptest servers without standing up public DNS.
// Counter-test for TestDelivery_RejectsLoopbackAtDialTime.
func TestDelivery_AllowsLoopbackWithEscape(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := ssrfTestRegistry(true, nil) // allowPrivate=true → demo mode
	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)
	r.Deliver(MakeEvent("fake.event", "evt_1", "1", time.Now(),
		map[string]string{"text": "hi"}))

	require.Eventually(t, func() bool { return hits.Load() == 1 },
		2*time.Second, 20*time.Millisecond,
		"with the escape hatch, loopback delivery must succeed")
}

// TestDelivery_DialBlocklistRanges verifies the per-range blocking. We
// can't bind a server to every range, so we ask the registry's
// dialContext callback directly with synthetic addresses and verify the
// classification — what would the Dialer have done? The actual dial
// path is exercised by the loopback test above; this one pins the
// per-range CIDR membership.
func TestDelivery_DialBlocklistRanges(t *testing.T) {
	r := NewWebhookRegistry() // strict default
	dial := r.dialContextForTest()

	cases := []struct {
		name string
		addr string
	}{
		{"IPv4 loopback", "127.0.0.1:80"},
		{"IPv6 loopback", "[::1]:80"},
		{"IPv4 link-local", "169.254.169.254:80"}, // AWS metadata service
		{"IPv4 private 10/8", "10.0.0.1:80"},
		{"IPv4 private 172.16/12", "172.16.0.1:80"},
		{"IPv4 private 192.168/16", "192.168.0.1:80"},
		{"IPv6 link-local", "[fe80::1]:80"},
		{"IPv6 ULA fc00::/7", "[fc00::1]:80"},
		{"IPv4-mapped-IPv6 loopback", "[::ffff:127.0.0.1]:80"},
		{"unspecified IPv4", "0.0.0.0:80"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := dial(ctx, "tcp", tc.addr)
			require.Error(t, err, "%s should be blocked at dial", tc.addr)
			assert.True(t,
				strings.Contains(err.Error(), "SSRF") || strings.Contains(err.Error(), "blocked"),
				"error should mention SSRF/blocked for %s; got: %v", tc.addr, err)
		})
	}
}

// TestDelivery_DialAllowsPublicIPs verifies the counter-test: a
// recognizably-public IP (8.8.8.8 in this case — Google DNS, won't be
// dialled to completion because we cancel the ctx, but should NOT be
// rejected by the SSRF guard).
func TestDelivery_DialAllowsPublicIPs(t *testing.T) {
	r := NewWebhookRegistry()
	dial := r.dialContextForTest()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := dial(ctx, "tcp", "8.8.8.8:443")
	// We expect either a context-deadline-exceeded or a connection error,
	// NOT an SSRF rejection.
	if err == nil {
		t.Skip("unexpectedly connected to 8.8.8.8 within 50ms; environment is too fast/cached")
	}
	assert.False(t,
		strings.Contains(err.Error(), "SSRF") || strings.Contains(err.Error(), "blocked"),
		"public IP must not be SSRF-rejected; got: %v", err)
}

// MakeEvent + the dial test helpers below reference the delivery-failure
// log channel — confirm a failed dial classifies correctly.
var _ = io.EOF // keep io import alive for potential future error checks

// TestDelivery_DoesNotFollowRedirects verifies that the webhook
// http.Client refuses to follow 3xx responses (ζ-2). Without this, a
// receiver could 302 to an internal address (127.0.0.1, 10.x, etc.)
// and bypass the dial-time SSRF guard via Go's default 10-redirect
// follow behavior.
//
// Per spec §"Webhook Security" → "SSRF prevention" L464: redirects
// MUST be explicitly disabled for outbound delivery POSTs.
func TestDelivery_DoesNotFollowRedirects(t *testing.T) {
	var hits, redirectHits atomic.Int32

	// Final destination — should NEVER be hit if redirects are disabled.
	finalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer finalSrv.Close()

	// Initial receiver — returns a 302 to finalSrv.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", finalSrv.URL)
		w.WriteHeader(http.StatusFound) // 302
	}))
	defer srv.Close()

	// Use the loopback escape so we can test the redirect specifically,
	// not the SSRF dial guard.
	r := ssrfTestRegistry(true, nil)
	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)
	r.Deliver(MakeEvent("fake.event", "evt_1", "1", time.Now(),
		map[string]string{"text": "hi"}))

	// Wait long enough for the deliver loop to retry-and-fail (3xx
	// classified as non-retryable per ζ-2 + retry semantics).
	time.Sleep(300 * time.Millisecond)

	assert.Equal(t, int32(1), hits.Load(),
		"initial receiver should be POSTed exactly once — 3xx is non-retryable per ζ-2; got %d", hits.Load())
	assert.Equal(t, int32(0), redirectHits.Load(),
		"redirect target MUST NOT be followed; got %d follow-up POSTs", redirectHits.Load())
}

// errIsContextOrConn returns true if the error is the kind of network
// failure we expect when a dial is allowed-by-SSRF but fails for other
// reasons (timeout, refused, etc.).
func errIsContextOrConn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

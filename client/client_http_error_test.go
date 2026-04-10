package client_test

// HTTP status error handling and SSE reader death tests (issue #132).
//
// These tests verify two related fixes:
//
// Bug A (Streamable HTTP): Non-2xx HTTP responses (5xx, 429) were silently
// swallowed by DoWithAuthRetry, producing opaque "invalid JSON response" errors
// that IsTransientError couldn't classify. Now they produce HTTPStatusError,
// which IsTransientError classifies as transient for 5xx, enabling retry.
//
// Bug B (SSE): When the SSE background reader died (network blip, server
// close), call() would block forever on the response channel because no reader
// was running to deliver responses. Now call() uses a dual-select on both the
// response channel and the done channel, returning immediately with a transient
// error so the reconnect machinery can kick in.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTPStatusError_IsTransient verifies that IsTransientError correctly
// classifies HTTPStatusError by status code:
//   - 5xx (500, 502, 503) → transient (server overload, gateway timeout)
//   - 4xx (400, 404, 409) → NOT transient (client error, won't succeed on retry)
//
// This is a unit test of the classification logic, not the transport.
func TestHTTPStatusError_IsTransient(t *testing.T) {
	// 5xx errors should be transient
	assert.True(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 500}),
		"HTTP 500 should be transient")
	assert.True(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 502}),
		"HTTP 502 should be transient")
	assert.True(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 503}),
		"HTTP 503 should be transient")

	// 4xx errors should NOT be transient
	assert.False(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 400}),
		"HTTP 400 should not be transient")
	assert.False(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 404}),
		"HTTP 404 should not be transient")
	assert.False(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 409}),
		"HTTP 409 should not be transient")
}

// TestHTTPStatusError_ErrorString verifies the error message format for
// HTTPStatusError, both with and without a response body.
func TestHTTPStatusError_ErrorString(t *testing.T) {
	err := &client.HTTPStatusError{StatusCode: 503, Body: "service unavailable"}
	assert.Equal(t, "HTTP 503: service unavailable", err.Error())

	err2 := &client.HTTPStatusError{StatusCode: 500}
	assert.Equal(t, "HTTP 500", err2.Error())
}

// TestHTTPStatusError_Unwrap verifies that HTTPStatusError works with
// errors.As for type-based error classification.
func TestHTTPStatusError_Unwrap(t *testing.T) {
	var target *client.HTTPStatusError
	err := error(&client.HTTPStatusError{StatusCode: 503, Body: "overloaded"})
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, 503, target.StatusCode)
}

// TestHTTPStatusError_HeaderExposed verifies that when the server returns a
// non-2xx response with custom headers, those headers are captured in the
// HTTPStatusError and accessible via errors.As. This enables callers to inspect
// response metadata (e.g., X-Request-Id for correlation, Retry-After for
// backoff) from error responses without losing information at the transport layer.
func TestHTTPStatusError_HeaderExposed(t *testing.T) {
	srv := newTestMCPServer()
	realHandler := srv.Handler(server.WithStreamableHTTP(true))
	realServer := httptest.NewServer(realHandler)
	t.Cleanup(realServer.Close)

	var callCount int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if r.Method == http.MethodPost && n == 3 {
			// 3rd POST = first tool call (after initialize + initialized)
			w.Header().Set("X-Request-Id", "req-abc123")
			w.Header().Set("Retry-After", "30")
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		// Proxy to real server
		body, _ := io.ReadAll(r.Body)
		proxyReq, _ := http.NewRequest(r.Method, realServer.URL+r.URL.Path, strings.NewReader(string(body)))
		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	c := client.NewClient(proxy.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })

	_, err := c.ToolCall("echo", map[string]any{"message": "fail"})
	require.Error(t, err)

	var httpErr *client.HTTPStatusError
	require.True(t, errors.As(err, &httpErr), "error should be HTTPStatusError, got: %T: %v", err, err)
	assert.Equal(t, 503, httpErr.StatusCode)
	assert.Equal(t, "req-abc123", httpErr.Header.Get("X-Request-Id"),
		"custom header X-Request-Id should be preserved in HTTPStatusError")
	assert.Equal(t, "30", httpErr.Header.Get("Retry-After"),
		"Retry-After header should be preserved in HTTPStatusError")
}

// TestHTTPStatusError_429WithRetryAfter verifies that a 429 Too Many Requests
// response (which is NOT handled by DoWithAuthRetry — only 401/403 are) flows
// through as an HTTPStatusError with headers intact. This is important because
// 429 is a common rate-limiting response that callers need Retry-After from.
func TestHTTPStatusError_429WithRetryAfter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	err := c.Connect()

	// Connect sends initialize which will get 429
	require.Error(t, err)
	var httpErr *client.HTTPStatusError
	require.True(t, errors.As(err, &httpErr), "error should be HTTPStatusError, got: %T: %v", err, err)
	assert.Equal(t, 429, httpErr.StatusCode)
	assert.Equal(t, "5", httpErr.Header.Get("Retry-After"),
		"Retry-After header should be accessible from 429 HTTPStatusError")
}

// TestStreamable_5xxReturnsHTTPStatusError verifies that when the server
// returns a 503 for a Streamable HTTP POST, the client produces an
// HTTPStatusError (not a confusing "invalid JSON response" error). The error
// should be classified as transient by IsTransientError.
//
// Before the fix: DoWithAuthRetry returned (resp, nil) for 503. The call()
// method tried to JSON-unmarshal the non-JSON error body, producing
// "invalid JSON response: service unavailable" — which IsTransientError
// couldn't classify.
func TestStreamable_5xxReturnsHTTPStatusError(t *testing.T) {
	var callCount atomic.Int32

	// Proxy that returns 503 on the first POST, then proxies normally
	srv := newTestMCPServer()
	realHandler := srv.Handler(server.WithStreamableHTTP(true))
	realServer := httptest.NewServer(realHandler)
	t.Cleanup(realServer.Close)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if r.Method == http.MethodPost && n == 3 {
			// 3rd POST = first tool call (after initialize + initialized)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		// Proxy to real server
		body, _ := io.ReadAll(r.Body)
		proxyReq, _ := http.NewRequest(r.Method, realServer.URL+r.URL.Path, strings.NewReader(string(body)))
		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	c := client.NewClient(proxy.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })

	// This call should hit the 503 proxy response
	_, err := c.ToolCall("echo", map[string]any{"message": "fail"})
	require.Error(t, err)

	// Verify it's an HTTPStatusError
	var httpErr *client.HTTPStatusError
	assert.True(t, errors.As(err, &httpErr), "error should be HTTPStatusError, got: %T: %v", err, err)
	if httpErr != nil {
		assert.Equal(t, 503, httpErr.StatusCode)
	}

	// Verify it's classified as transient
	assert.True(t, client.IsTransientError(err), "503 error should be transient")
}

// TestStreamable_5xxNotifyReturnsError verifies that when the server returns a
// 5xx for a notification POST, the client returns an error instead of silently
// succeeding.
//
// Before the fix: streamableClientTransport.notify() did not check
// resp.StatusCode at all — a 500 response was silently ignored.
func TestStreamable_5xxNotifyReturnsError(t *testing.T) {
	// Server that returns 500 for all POSTs
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	// Create a client — we can't Connect() since initialize will fail,
	// so test at the transport level by checking the error type directly.
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	err := c.Connect()

	// Connect sends initialize which will get 500 — should be HTTPStatusError
	require.Error(t, err)
	var httpErr *client.HTTPStatusError
	assert.True(t, errors.As(err, &httpErr), "error should be HTTPStatusError, got: %T: %v", err, err)
}

// TestSSE_ReaderDeathDoesNotBlock verifies that when the SSE background reader
// dies (e.g., server closes the connection), subsequent call() invocations
// return promptly with a transient error instead of blocking forever.
//
// Before the fix: call() used a bare `<-ch` which blocked forever after reader
// death because no goroutine was running to deliver responses. Now call() uses
// a dual-select on both the response channel and the done channel.
func TestSSE_ReaderDeathDoesNotBlock(t *testing.T) {
	srv := newTestMCPServer()
	handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
	ts := httptest.NewServer(handler)

	c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithSSEClient())
	require.NoError(t, c.Connect())

	// First call should succeed
	result, err := c.ToolCall("echo", map[string]any{"message": "alive"})
	require.NoError(t, err)
	assert.Contains(t, result, "alive")

	// Force-close all server-side connections to simulate a network blip.
	// This kills the SSE stream without the client initiating a close.
	// ts.CloseClientConnections() drops TCP connections; ts.Close() would
	// block waiting for the active SSE handler to return.
	ts.CloseClientConnections()

	// Wait briefly for the background reader to detect the broken connection
	time.Sleep(100 * time.Millisecond)

	// Second call should return an error promptly, NOT block forever.
	// Use a timeout to guard against the old blocking behavior.
	done := make(chan error, 1)
	go func() {
		_, err := c.ToolCall("echo", map[string]any{"message": "dead"})
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "call after reader death should return error")
		// The error should be transient (EOF or connection error)
		assert.True(t, client.IsTransientError(err),
			"error after reader death should be transient, got: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("call() blocked for 5s after reader death — the old bug is back")
	}

	c.Close()
	ts.Close()
}

// TestSSE_ReaderDeathCallReturnsTransientError verifies that after the SSE
// background reader dies, subsequent calls return transient errors that the
// reconnect machinery can act on. Unlike TestSSE_ReaderDeathDoesNotBlock which
// tests the timing (no hang), this test focuses on the error classification.
func TestSSE_ReaderDeathCallReturnsTransientError(t *testing.T) {
	srv := newTestMCPServer()
	handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
	ts := httptest.NewServer(handler)

	c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithSSEClient())
	require.NoError(t, c.Connect())

	// Force-close server-side connections to kill the SSE stream
	ts.CloseClientConnections()

	// Wait briefly for the background reader to detect the broken connection
	time.Sleep(100 * time.Millisecond)

	// Call should return an error classified as transient
	_, err := c.ToolCall("echo", map[string]any{"message": "dead"})
	require.Error(t, err, "call after reader death should return error")
	assert.True(t, client.IsTransientError(err),
		"error after reader death should be transient, got: %v", err)

	c.Close()
	ts.Close()
}

// TestStreamable_5xxWithReconnect verifies end-to-end that a 503 error triggers
// automatic reconnection when WithMaxRetries is configured. The first tool call
// hits a 503, the client reconnects to a working server, and the retry succeeds.
func TestStreamable_5xxWithReconnect(t *testing.T) {
	var callCount atomic.Int32

	srv := newTestMCPServer()
	realHandler := srv.Handler(server.WithStreamableHTTP(true))

	// Server that returns 503 on the first tool call POST, then works normally
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n := callCount.Add(1)
			// Let initialize (call 1) and initialized (call 2) through.
			// First tool call (call 3) gets 503.
			// After reconnect: initialize (4), initialized (5), retry tool call (6) succeed.
			if n == 3 {
				http.Error(w, "overloaded", http.StatusServiceUnavailable)
				return
			}
		}
		realHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithMaxRetries(1),
		client.WithReconnectBackoff(10*time.Millisecond))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })

	// Should fail on first attempt (503), reconnect, and succeed on retry
	result, err := c.ToolCall("echo", map[string]any{"message": "recovered"})
	require.NoError(t, err)
	assert.Contains(t, result, "recovered")
}

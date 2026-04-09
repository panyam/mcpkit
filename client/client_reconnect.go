package client

// Client reconnection with exponential backoff. When a transport call fails
// with a transient error (network disconnect, EOF, connection reset), the
// client automatically tears down the old transport, creates a new one,
// re-runs the MCP initialize handshake, and retries the failed request.
//
// Enable via WithMaxRetries(n). Disabled by default (maxRetries=0).

import (
	core "github.com/panyam/mcpkit/core"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"time"
)

// WithMaxRetries sets the maximum number of reconnection attempts on
// transient transport failure. Default 0 (reconnection disabled).
// Each retry includes a full reconnect + initialize handshake.
//
// Example:
//
//	client := mcpkit.NewClient(url, info,
//	    mcpkit.WithMaxRetries(3),
//	    mcpkit.WithReconnectBackoff(time.Second),
//	)
func WithMaxRetries(n int) ClientOption {
	return func(c *Client) { c.maxRetries = n }
}

// WithReconnectBackoff sets the base delay for exponential backoff between
// reconnection attempts. Default 1s. Actual delay is base * 2^attempt + jitter.
func WithReconnectBackoff(d time.Duration) ClientOption {
	return func(c *Client) { c.baseDelay = d }
}

// reconnect tears down the current transport and re-establishes the connection
// including the MCP initialize handshake. Called internally by retryWithReconnect.
func (c *Client) reconnect() error {
	// Close existing transport
	if c.transport != nil {
		c.transport.close()
		c.transport = nil
	}

	// Re-create transport with handlers and options
	if c.useSSE {
		st := newSSEClientTransport(c.url, c.tokenSource)
		st.serverReqHandler = c.HandleServerRequest
		if c.onNotify != nil {
			st.notifyHandler = c.makeNotifyAdapter()
		}
		c.transport = st
	} else {
		st := newStreamableClientTransport(c.url, c.tokenSource)
		st.client = c
		st.serverReqHandler = c.HandleServerRequest
		st.enableGetSSE = c.enableGetSSE
		if c.onNotify != nil {
			st.notifyHandler = c.makeNotifyAdapter()
		}
		c.transport = st
	}

	// Re-wrap with logging if configured
	if c.logger != nil {
		c.transport = &loggingTransport{inner: c.transport, logger: c.logger}
	}

	// Connect transport
	if err := c.transport.connect(); err != nil {
		return err
	}

	// Re-initialize MCP handshake (use transport directly, not rawCall which
	// would trigger reconnection recursion)
	initReq := core.Request{
		JSONRPC: "2.0",
		ID:      marshalID(c.nextRequestID()),
		Method:  "initialize",
	}
	initReq.Params, _ = json.Marshal(initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    core.ClientCapabilities{},
		ClientInfo:      c.info,
	})
	data, _ := json.Marshal(initReq)
	resp, err := c.transport.call(data)
	if err != nil {
		return err
	}

	// Extract server info
	var initResult core.InitializeResult
	if err := json.Unmarshal(resp.Result, &initResult); err == nil {
		c.ServerInfo = initResult.ServerInfo
	}

	// Send initialized notification (directly, not via notifyMethod)
	notifReq := core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	data, _ = json.Marshal(notifReq)
	if err := c.transport.notify(data); err != nil {
		return err
	}

	// Re-open GET SSE stream if it was enabled (best-effort, don't fail reconnect)
	if c.enableGetSSE {
		if st := c.unwrapStreamableTransport(); st != nil {
			st.openGetSSEStream()
		}
	}

	return nil
}

// retryWithReconnect attempts reconnection with exponential backoff, then
// retries the given function. Returns the first successful result or the
// last error after exhausting retries.
func (c *Client) retryWithReconnect(fn func() (*rpcResponse, error)) (*rpcResponse, error) {
	baseDelay := c.baseDelay
	if baseDelay == 0 {
		baseDelay = time.Second
	}

	var lastErr error
	for attempt := range c.maxRetries {
		// Exponential backoff with jitter
		delay := baseDelay * time.Duration(1<<uint(attempt))
		jitter := time.Duration(rand.Int64N(int64(delay / 4)))
		time.Sleep(delay + jitter)

		if err := c.reconnect(); err != nil {
			lastErr = err
			continue
		}

		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		if !IsTransientError(err) {
			return nil, err // terminal error, stop retrying
		}
		lastErr = err
	}
	return nil, lastErr
}

// retryNotifyWithReconnect is like retryWithReconnect but for notifications
// (no response expected).
func (c *Client) retryNotifyWithReconnect(fn func() error) error {
	baseDelay := c.baseDelay
	if baseDelay == 0 {
		baseDelay = time.Second
	}

	var lastErr error
	for attempt := range c.maxRetries {
		delay := baseDelay * time.Duration(1<<uint(attempt))
		jitter := time.Duration(rand.Int64N(int64(delay / 4)))
		time.Sleep(delay + jitter)

		if err := c.reconnect(); err != nil {
			lastErr = err
			continue
		}

		if err := fn(); err == nil {
			return nil
		} else if !IsTransientError(err) {
			return err
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// isTransientError returns true if the error indicates a recoverable transport
// failure that may succeed on reconnection. Network errors (EOF, connection
// reset, refused) are transient. Auth errors (401/403) and JSON-RPC errors
// are NOT transient — the server responded, just said no.
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}

	// Auth errors are terminal (server responded with clear rejection)
	var authErr *ClientAuthError
	if errors.As(err, &authErr) {
		return false
	}

	// HTTP 5xx errors are transient (server overload, gateway timeout, etc.)
	// 4xx errors (other than 401/403 handled above) are terminal.
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}

	// Network-level errors
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Net package errors (connection refused, reset, etc.)
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}

	// String-based fallback for wrapped errors
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe")
}

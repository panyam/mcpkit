package mcpkit

// Client reconnection with exponential backoff. When a transport call fails
// with a transient error (network disconnect, EOF, connection reset), the
// client automatically tears down the old transport, creates a new one,
// re-runs the MCP initialize handshake, and retries the failed request.
//
// Enable via WithMaxRetries(n). Disabled by default (maxRetries=0).

import (
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

	// Re-create transport
	if c.useSSE {
		c.transport = newSSEClientTransport(c.url, c.tokenSource)
	} else {
		c.transport = newStreamableClientTransport(c.url, c.tokenSource)
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
	initBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextRequestID(),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      c.info,
		},
	}
	data, _ := json.Marshal(initBody)
	resp, err := c.transport.call(data)
	if err != nil {
		return err
	}

	// Extract server info
	if result, ok := resp.Result.(map[string]any); ok {
		if si, ok := result["serverInfo"].(map[string]any); ok {
			c.ServerInfo.Name, _ = si["name"].(string)
			c.ServerInfo.Version, _ = si["version"].(string)
		}
	}

	// Send initialized notification (directly, not via notifyMethod)
	notifBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	data, _ = json.Marshal(notifBody)
	return c.transport.notify(data)
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
		if !isTransientError(err) {
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
		} else if !isTransientError(err) {
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
func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	// Auth errors are terminal (server responded with clear rejection)
	var authErr *ClientAuthError
	if errors.As(err, &authErr) {
		return false
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

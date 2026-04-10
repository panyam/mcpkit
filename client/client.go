package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	conc "github.com/panyam/gocurrent"
	core "github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// clientTransport abstracts the transport layer for the MCP client.
type clientTransport interface {
	// connect establishes the transport connection.
	connect() error
	// call sends a JSON-RPC request and returns the response.
	call(data []byte) (*rpcResponse, error)
	// notify sends a JSON-RPC notification (no response expected).
	notify(data []byte) error
	// close shuts down the transport.
	close() error
	// getSessionID returns the current session ID (empty if none).
	getSessionID() string
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithSSEClient configures the client to use SSE transport instead of Streamable HTTP.
// The URL should point to the SSE endpoint (e.g., "http://localhost:8787/mcp/sse").
func WithSSEClient() ClientOption {
	return func(c *Client) { c.useSSE = true }
}

// WithClientBearerToken sets a static bearer token for all client requests.
func WithClientBearerToken(token string) ClientOption {
	return func(c *Client) { c.tokenSource = &staticToken{token: token} }
}

// staticToken is a TokenSource that always returns the same token.
type staticToken struct{ token string }

func (s *staticToken) Token() (string, error) { return s.token, nil }

// WithTokenSource sets a dynamic token source for all client requests.
// Use this for OAuth flows where tokens are refreshed automatically.
func WithTokenSource(ts core.TokenSource) ClientOption {
	return func(c *Client) { c.tokenSource = ts }
}

// SamplingHandler handles a server-to-client sampling/createMessage request.
// The client performs LLM inference and returns the result.
type SamplingHandler func(context.Context, core.CreateMessageRequest) (core.CreateMessageResult, error)

// ElicitationHandler handles a server-to-client elicitation/create request.
// The client prompts the user for input and returns the result.
type ElicitationHandler func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error)

// WithSamplingHandler registers a handler for server-to-client sampling requests.
// When set, the client advertises the "sampling" capability during initialization.
func WithSamplingHandler(h SamplingHandler) ClientOption {
	return func(c *Client) { c.samplingHandler = h }
}

// WithElicitationHandler registers a handler for server-to-client elicitation requests.
// When set, the client advertises the "elicitation" capability during initialization.
func WithElicitationHandler(h ElicitationHandler) ClientOption {
	return func(c *Client) { c.elicitationHandler = h }
}

// WithNotificationCallback sets a callback for server-to-client notifications
// (logging, progress, resource updates). Works across all transports.
func WithNotificationCallback(fn func(method string, params any)) ClientOption {
	return func(c *Client) { c.onNotify = fn }
}

// WithGetSSEStream enables a background GET SSE stream on the Streamable HTTP
// endpoint after Connect(). The stream receives server-initiated notifications
// (list-changed, log messages, resource updates) that arrive outside POST
// request-response cycles. Only applies to Streamable HTTP transport; ignored
// for SSE and in-memory transports.
//
// The notification callback (set via WithNotificationCallback) must be
// goroutine-safe when WithGetSSEStream is enabled, as notifications may arrive
// concurrently from both the GET SSE stream and POST SSE responses.
func WithGetSSEStream() ClientOption {
	return func(c *Client) { c.enableGetSSE = true }
}

// WithModifyRequest sets a callback that is invoked on every outgoing HTTP
// request before authentication headers are applied. Use it to add custom
// headers (API keys, tracing IDs, tenant identifiers) without needing a
// custom http.RoundTripper.
//
// The callback must not modify the request body or URL.
// Only applies to HTTP transports (Streamable HTTP, SSE); ignored for
// stdio and in-process transports.
//
// Example:
//
//	c := client.NewClient(url, info,
//	    client.WithModifyRequest(func(req *http.Request) {
//	        req.Header.Set("X-Tenant-ID", "acme")
//	        req.Header.Set("X-Request-ID", uuid.New().String())
//	    }),
//	)
func WithModifyRequest(fn func(*http.Request)) ClientOption {
	return func(c *Client) { c.modifyRequest = fn }
}

// WithContentChunkHandler sets a callback for streaming tool content chunks.
// The callback is invoked for each content chunk notification received during
// tool execution (method matching the server's configured content chunk method,
// default "notifications/tools/content_chunk").
//
// If not set, content chunk notifications are silently ignored and the client
// relies on the final ToolResult for the complete response.
func WithContentChunkHandler(fn func(chunk core.ContentChunk)) ClientOption {
	return func(c *Client) { c.onContentChunk = fn }
}

// WithCommandTransport configures the client to spawn a subprocess MCP server
// and communicate over stdin/stdout. A fresh process is started on each
// Connect() (and on each reconnection if WithMaxRetries is set).
//
// Example:
//
//	c := client.NewClient("", info,
//	    client.WithCommandTransport("python", []string{"my_server.py"},
//	        client.WithEnv("DEBUG=1"),
//	    ),
//	)
//	err := c.Connect()
func WithCommandTransport(name string, args []string, opts ...CommandOption) ClientOption {
	return func(c *Client) {
		c.commandName = name
		c.commandArgs = args
		c.commandOpts = opts
	}
}

// WithConnectTimeout sets a deadline for Connect() to complete. This covers
// both the transport connection (subprocess start, SSE stream open) and the
// MCP initialize handshake. If the timeout expires, Connect() returns an error
// immediately instead of blocking indefinitely.
//
// This is especially important for CommandTransport: if the subprocess starts
// but doesn't speak Content-Length framed JSON-RPC (e.g., wrong mode, missing
// env vars), Connect() would block forever without a timeout.
//
// Default is 0 (no timeout).
func WithConnectTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.connectTimeout = d }
}

// WithClientKeepalive enables application-level keepalive pings. The client
// periodically sends JSON-RPC ping requests to the server. If maxFailures
// consecutive pings fail (timeout or error), the client triggers reconnection
// (if retries are configured) or closes.
func WithClientKeepalive(interval time.Duration, maxFailures int) ClientOption {
	return func(c *Client) {
		c.keepaliveInterval = interval
		c.keepaliveMaxFails = maxFailures
	}
}

// WithExtension advertises support for an extension during the initialize
// handshake. The extension ID and capability are included in the client's
// capabilities.extensions map, allowing the server to detect client support
// via core.ClientSupportsExtension(ctx, id) in tool handlers.
func WithExtension(id string, cap core.ClientExtensionCap) ClientOption {
	return func(c *Client) {
		if c.extensions == nil {
			c.extensions = make(map[string]core.ClientExtensionCap)
		}
		c.extensions[id] = cap
	}
}

// WithUIExtension is a convenience wrapper that advertises MCP Apps
// (io.modelcontextprotocol/ui) support with the standard app MIME type.
func WithUIExtension() ClientOption {
	return WithExtension(core.UIExtensionID, core.ClientExtensionCap{
		MIMETypes: []string{core.AppMIMEType},
	})
}

// WithTransport sets a core.Transport for the client, bypassing the default
// HTTP transport creation. Use with server.NewInProcessTransport for testing
// or embedded scenarios.
//
// Example:
//
//	transport := server.NewInProcessTransport(srv)
//	c := client.New("memory://", info, client.WithTransport(transport))
func WithTransport(t core.Transport) ClientOption {
	return func(c *Client) {
		c.transport = &coreTransportAdapter{inner: t}
	}
}

// coreTransportAdapter wraps a core.Transport into the internal clientTransport
// interface. Handles JSON marshaling/unmarshaling between the typed core interface
// and the byte-oriented internal interface used by the client.
type coreTransportAdapter struct {
	inner core.Transport
}

func (a *coreTransportAdapter) connect() error {
	return a.inner.Connect(context.Background())
}

func (a *coreTransportAdapter) call(data []byte) (*rpcResponse, error) {
	var req core.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	resp, err := a.inner.Call(context.Background(), &req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	// core.Response and rpcResponse have identical field types now
	return &rpcResponse{
		JSONRPC: resp.JSONRPC,
		ID:      resp.ID,
		Result:  resp.Result,
		Error:   resp.Error,
	}, nil
}

func (a *coreTransportAdapter) notify(data []byte) error {
	var req core.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("invalid notification: %w", err)
	}
	return a.inner.Notify(context.Background(), &req)
}

func (a *coreTransportAdapter) close() error {
	return a.inner.Close()
}

func (a *coreTransportAdapter) getSessionID() string {
	return a.inner.SessionID()
}

// Client is an MCP client that communicates over Streamable HTTP or SSE.
type Client struct {
	url         string
	info        core.ClientInfo
	useSSE      bool
	tokenSource core.TokenSource
	nextID      int
	mu          sync.Mutex
	transport   clientTransport
	logger      *log.Logger // optional transport logging (nil = disabled)

	// Extension support
	extensions       map[string]core.ClientExtensionCap // client extensions to advertise in initialize
	serverExtensions map[string]json.RawMessage         // parsed from server's initialize response

	// Server-to-client request handlers
	samplingHandler    SamplingHandler
	elicitationHandler ElicitationHandler

	// Reconnection settings (zero values = disabled)
	maxRetries int
	baseDelay  time.Duration

	// ServerInfo is populated after Connect.
	ServerInfo core.ServerInfo

	// onNotify is an optional callback for server-to-client notifications.
	// Used by all transports: in-memory (inline), SSE (background reader),
	// and Streamable HTTP (POST SSE responses + optional GET SSE stream).
	onNotify func(method string, params any)

	// enableGetSSE opts into the background GET SSE stream (Streamable HTTP only).
	enableGetSSE bool

	// onContentChunk is an optional callback for streaming tool content chunks.
	onContentChunk func(chunk core.ContentChunk)

	// lastEventID tracks the most recent SSE event ID received from the server.
	// Used to send Last-Event-ID header on reconnection for stream resumption.
	// Written by background SSE readers, read during reconnection.
	lastEventID atomic.Value // stores string

	// Client keepalive: periodic ping to detect dead server.
	keepaliveInterval time.Duration     // 0 = disabled
	keepaliveMaxFails int               // max consecutive failures before close/reconnect
	keepaliveCancel   context.CancelFunc // cancels keepalive goroutine

	// Stdio transport fields (set by WithStdioTransport).
	stdioReader io.Reader
	stdioWriter io.Writer

	// ModifyRequest hook for outgoing HTTP requests (set by WithModifyRequest).
	modifyRequest func(*http.Request)

	// Command transport fields (set by WithCommandTransport).
	commandName string
	commandArgs []string
	commandOpts []CommandOption

	// connectTimeout limits how long Connect() waits for the transport to
	// become ready (process start + initial handshake). Zero means no timeout.
	// Particularly important for CommandTransport where a misconfigured
	// subprocess may start but never speak the expected protocol.
	connectTimeout time.Duration
}

// NewClient creates a new MCP client targeting the given server URL.
// By default uses Streamable HTTP. Use WithSSEClient() for SSE transport.
// Call Connect() to perform the protocol handshake.
func NewClient(url string, info core.ClientInfo, opts ...ClientOption) *Client {
	c := &Client{
		url:    url,
		info:   info,
		nextID: 1,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// defaultCommandConnectTimeout is the default connect timeout for command and
// stdio transports. These communicate over pipes where a misconfigured
// subprocess can block forever (e.g., started in HTTP mode instead of stdio).
// HTTP transports have their own network-level timeouts and don't need this.
const defaultCommandConnectTimeout = 30 * time.Second

// Connect establishes the transport and performs the MCP initialize handshake.
//
// For command and stdio transports, Connect is bounded by a default 30s timeout
// to prevent indefinite hangs when the subprocess doesn't speak the expected
// protocol. Override with WithConnectTimeout. HTTP transports are not bounded
// by this default (set WithConnectTimeout explicitly if needed).
func (c *Client) Connect() error {
	timeout := c.connectTimeout
	// Auto-apply default timeout for command/stdio transports.
	if timeout <= 0 && c.isSubprocessTransport() {
		timeout = defaultCommandConnectTimeout
	}
	if timeout > 0 {
		done := make(chan error, 1)
		go func() { done <- c.doConnect() }()
		select {
		case err := <-done:
			return err
		case <-time.After(timeout):
			return fmt.Errorf("connect timed out after %s: subprocess started but did not complete MCP handshake — verify the server is in stdio mode (e.g., STDIO=1 env var) and check stderr for startup errors", timeout)
		}
	}
	return c.doConnect()
}

// isSubprocessTransport returns true if the client is configured to use a
// command or stdio transport (pipe-based, no network timeouts).
func (c *Client) isSubprocessTransport() bool {
	return c.commandName != "" || c.stdioReader != nil ||
		(c.transport != nil && isCommandOrStdioAdapter(c.transport))
}

func isCommandOrStdioAdapter(t clientTransport) bool {
	adapter, ok := t.(*coreTransportAdapter)
	if !ok {
		return false
	}
	switch adapter.inner.(type) {
	case *CommandTransport, *StdioTransport:
		return true
	}
	return false
}

// doConnect performs the actual transport connection and MCP handshake.
func (c *Client) doConnect() error {
	// Create transport (skip if already set, e.g., by WithInMemoryServer)
	if c.transport == nil {
		if c.commandName != "" {
			ct := NewCommandTransport(c.commandName, c.commandArgs, c.commandOpts...)
			ct.serverReqHandler = func(_ context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}
			if c.onNotify != nil {
				ct.notifyHandler = func(method string, params []byte) {
					var parsed any
					if len(params) > 0 {
						json.Unmarshal(params, &parsed)
					}
					c.onNotify(method, parsed)
				}
			}
			c.transport = &coreTransportAdapter{inner: ct}
		} else if c.stdioReader != nil && c.stdioWriter != nil {
			st := NewStdioTransport(c.stdioReader, c.stdioWriter)
			st.serverReqHandler = func(_ context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}
			if c.onNotify != nil {
				st.notifyHandler = func(method string, params []byte) {
					var parsed any
					if len(params) > 0 {
						json.Unmarshal(params, &parsed)
					}
					c.onNotify(method, parsed)
				}
			}
			c.transport = &coreTransportAdapter{inner: st}
		} else if c.useSSE {
			st := newSSEClientTransport(c.url, c.tokenSource)
			st.serverReqHandler = c.HandleServerRequest
			st.modifyReq = c.modifyRequest
			if c.onNotify != nil || c.onContentChunk != nil {
				st.notifyHandler = c.makeNotifyAdapter()
			}
			c.transport = st
		} else {
			st := newStreamableClientTransport(c.url, c.tokenSource)
			st.client = c
			st.serverReqHandler = c.HandleServerRequest
			st.enableGetSSE = c.enableGetSSE
			st.modifyReq = c.modifyRequest
			if c.onNotify != nil || c.onContentChunk != nil {
				st.notifyHandler = c.makeNotifyAdapter()
			}
			c.transport = st
		}

		// Wrap with logging if configured
		if c.logger != nil {
			c.transport = &loggingTransport{inner: c.transport, logger: c.logger}
		}
	}

	if err := c.transport.connect(); err != nil {
		return fmt.Errorf("transport connect: %w", err)
	}

	// Build client capabilities based on registered handlers
	caps := core.ClientCapabilities{}
	if c.samplingHandler != nil {
		caps.Sampling = &struct{}{}
	}
	if c.elicitationHandler != nil {
		caps.Elicitation = &struct{}{}
	}
	if len(c.extensions) > 0 {
		caps.Extensions = make(map[string]core.ClientExtensionCap, len(c.extensions))
		for id, cap := range c.extensions {
			caps.Extensions[id] = cap
		}
	}

	// Initialize handshake
	resp, err := c.rawCall("initialize", initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    caps,
		ClientInfo:      c.info,
	})
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	// Extract server info and capabilities from initialize response
	var initResult core.InitializeResult
	if err := json.Unmarshal(resp.Result, &initResult); err == nil {
		c.ServerInfo = initResult.ServerInfo
		if initResult.Capabilities.Extensions != nil {
			c.serverExtensions = make(map[string]json.RawMessage, len(initResult.Capabilities.Extensions))
			for id, ext := range initResult.Capabilities.Extensions {
				if raw, err := json.Marshal(ext); err == nil {
					c.serverExtensions[id] = raw
				}
			}
		}
	}

	// Send initialized notification. Non-fatal: the initialize handshake
	// already completed successfully. Some servers may not accept notifications
	// at this endpoint, or may return errors for notifications.
	if err := c.notifyMethod("notifications/initialized", nil); err != nil {
		// Log but don't fail — the session is usable regardless.
		if c.logger != nil {
			c.logger.Printf("notifications/initialized: %v (non-fatal)", err)
		}
	}

	// Open GET SSE stream for server-initiated notifications (Streamable HTTP only).
	// Non-fatal: the client can still function via POST-only mode.
	if c.enableGetSSE {
		if st := c.unwrapStreamableTransport(); st != nil {
			st.openGetSSEStream()
		}
	}

	// Start client keepalive if configured
	c.startKeepalive()

	return nil
}

// startKeepalive starts the client-side keepalive goroutine if configured.
// Sends periodic JSON-RPC ping requests to the server.
func (c *Client) startKeepalive() {
	if c.keepaliveInterval <= 0 {
		return
	}
	maxFails := c.keepaliveMaxFails
	if maxFails <= 0 {
		maxFails = 3
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.keepaliveCancel = cancel

	go func() {
		ticker := time.NewTicker(c.keepaliveInterval)
		defer ticker.Stop()

		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := c.rawCall("ping", nil)
				if err != nil {
					failures++
					if failures >= maxFails {
						// Trigger reconnection or close
						if c.maxRetries > 0 {
							c.transport.close()
						}
						return
					}
				} else {
					failures = 0
				}
			}
		}
	}()
}

// Close terminates the client session and transport.
func (c *Client) Close() error {
	if c.keepaliveCancel != nil {
		c.keepaliveCancel()
	}
	if c.transport != nil {
		return c.transport.close()
	}
	return nil
}

// unwrapStreamableTransport returns the underlying *streamableClientTransport
// if the client uses Streamable HTTP. Peels through loggingTransport wrappers.
// Returns nil for SSE and in-memory transports.
func (c *Client) unwrapStreamableTransport() *streamableClientTransport {
	t := c.transport
	if lt, ok := t.(*loggingTransport); ok {
		t = lt.inner
	}
	if st, ok := t.(*streamableClientTransport); ok {
		return st
	}
	return nil
}

// makeNotifyAdapter creates a transport-level notification handler that
// unmarshals JSON params and delegates to the client's onNotify callback.
// Also intercepts content chunk notifications for the onContentChunk handler.
func (c *Client) makeNotifyAdapter() func(string, json.RawMessage) {
	return func(method string, params json.RawMessage) {
		// Intercept content chunk notifications for the dedicated handler.
		// Matches the default method or any custom method containing "content_chunk".
		if c.onContentChunk != nil && method == core.DefaultContentChunkMethod {
			var chunk core.ContentChunk
			if json.Unmarshal(params, &chunk) == nil {
				c.onContentChunk(chunk)
				return // Don't also deliver to generic onNotify
			}
		}
		if c.onNotify != nil {
			var parsed any
			if len(params) > 0 {
				json.Unmarshal(params, &parsed)
			}
			c.onNotify(method, parsed)
		}
	}
}

// handleServerRequest dispatches an incoming server-to-client JSON-RPC request
// to the appropriate registered handler (sampling or elicitation).
// Returns a JSON-RPC response to send back to the server.
func (c *Client) HandleServerRequest(req *core.Request) *core.Response {
	switch req.Method {
	case "sampling/createMessage":
		if c.samplingHandler == nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "sampling not supported")
		}
		var params core.CreateMessageRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams, "invalid sampling params: "+err.Error())
		}
		result, err := c.samplingHandler(context.Background(), params)
		if err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInternal, err.Error())
		}
		return core.NewResponse(req.ID, result)

	case "elicitation/create":
		if c.elicitationHandler == nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "elicitation not supported")
		}
		var params core.ElicitationRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams, "invalid elicitation params: "+err.Error())
		}
		result, err := c.elicitationHandler(context.Background(), params)
		if err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInternal, err.Error())
		}
		return core.NewResponse(req.ID, result)

	default:
		return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "unknown server request: "+req.Method)
	}
}

// SessionID returns the current session ID.
func (c *Client) SessionID() string {
	if c.transport != nil {
		return c.transport.getSessionID()
	}
	return ""
}

// SetTransport sets the transport for the client. Use when the transport needs
// to reference the client (e.g., InProcessTransport with ServerRequestHandler
// that delegates to the client's sampling/elicitation handlers).
// Must be called before Connect().
func (c *Client) SetTransport(t core.Transport) {
	c.transport = &coreTransportAdapter{inner: t}
}

// URL returns the client's target URL.
func (c *Client) URL() string { return c.url }

// SetURL updates the client's target URL. Used in reconnection tests
// to simulate DNS/load balancer changes.
func (c *Client) SetURL(url string) { c.url = url }

// Call makes a JSON-RPC call and returns the parsed response.
func (c *Client) Call(method string, params any) (*CallResult, error) {
	resp, err := c.rawCall(method, params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return &CallResult{Raw: resp.Result}, nil
}

// CallResult holds the raw JSON result from a JSON-RPC call.
type CallResult struct {
	Raw json.RawMessage
}

// JSON returns the result as indented JSON.
func (r *CallResult) JSON() string {
	var v any
	json.Unmarshal(r.Raw, &v)
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

// Unmarshal decodes the result into the given value.
func (r *CallResult) Unmarshal(v any) error {
	return json.Unmarshal(r.Raw, v)
}

// --- Convenience methods ---

// ToolCallTyped invokes a tool and unmarshals the structured content into T.
// This is for tools that declare an OutputSchema and return StructuredContent.
// Returns an error if the tool has no structured content or if unmarshaling fails.
//
// Example:
//
//	type SearchResult struct {
//	    Results []string `json:"results"`
//	    Total   int      `json:"total"`
//	}
//	result, err := client.ToolCallTyped[SearchResult](c, "search", map[string]any{"query": "test"})
func ToolCallTyped[T any](c *Client, name string, args any) (T, error) {
	var zero T
	result, err := c.Call("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return zero, err
	}

	// Parse the tool result to extract structuredContent
	var toolResult struct {
		IsError           bool            `json:"isError"`
		Content           []core.Content  `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(result.Raw, &toolResult); err != nil {
		return zero, fmt.Errorf("unmarshal tool result: %w", err)
	}
	if toolResult.IsError {
		if len(toolResult.Content) > 0 {
			return zero, fmt.Errorf("tool error: %s", toolResult.Content[0].Text)
		}
		return zero, fmt.Errorf("tool error (no content)")
	}
	if toolResult.StructuredContent == nil {
		return zero, fmt.Errorf("tool %q returned no structured content", name)
	}

	var typed T
	if err := json.Unmarshal(toolResult.StructuredContent, &typed); err != nil {
		return zero, fmt.Errorf("unmarshal structured content: %w", err)
	}
	return typed, nil
}

// ToolCall invokes a tool and returns the first text content.
func (c *Client) ToolCall(name string, args any) (string, error) {
	result, err := c.Call("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	return extractToolText(result.Raw)
}

// ReadResource reads a resource by URI and returns the first text content.
func (c *Client) ReadResource(uri string) (string, error) {
	result, err := c.Call("resources/read", map[string]string{"uri": uri})
	if err != nil {
		return "", err
	}
	return extractResourceText(result.Raw)
}

// SubscribeResource subscribes to change notifications for a resource URI.
// The server will send notifications/resources/updated when the resource changes.
func (c *Client) SubscribeResource(uri string) error {
	_, err := c.Call("resources/subscribe", map[string]string{"uri": uri})
	return err
}

// UnsubscribeResource removes a subscription for a resource URI.
func (c *Client) UnsubscribeResource(uri string) error {
	_, err := c.Call("resources/unsubscribe", map[string]string{"uri": uri})
	return err
}

// ListTools returns all registered tool definitions.
func (c *Client) ListTools() ([]core.ToolDef, error) {
	result, err := c.Call("tools/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

// ListToolsForModel returns tools visible to the LLM, filtering out tools
// that are only visible to apps (visibility: ["app"]). Tools with no visibility
// set (nil/empty) are included — the default means visible to both model and app.
// This is a client-side convenience; the server always returns all tools.
func (c *Client) ListToolsForModel() ([]core.ToolDef, error) {
	tools, err := c.ListTools()
	if err != nil {
		return nil, err
	}
	var filtered []core.ToolDef
	for _, t := range tools {
		if isModelVisible(t) {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// isModelVisible returns true if the tool should be visible to the LLM.
// A tool is model-visible if it has no visibility set (default) or if its
// visibility list includes "model".
func isModelVisible(t core.ToolDef) bool {
	if t.Meta == nil || t.Meta.UI == nil || len(t.Meta.UI.Visibility) == 0 {
		return true // default: visible to model
	}
	for _, v := range t.Meta.UI.Visibility {
		if v == core.UIVisibilityModel {
			return true
		}
	}
	return false
}

// ServerSupportsExtension checks whether the server advertised support for the
// given extension ID in its initialize response. Call after Connect().
func (c *Client) ServerSupportsExtension(id string) bool {
	_, ok := c.serverExtensions[id]
	return ok
}

// ServerSupportsUI checks whether the server advertised MCP Apps
// (io.modelcontextprotocol/ui) support. Convenience wrapper around
// ServerSupportsExtension.
func (c *Client) ServerSupportsUI() bool {
	return c.ServerSupportsExtension(core.UIExtensionID)
}

// ListResources returns all registered static resources.
func (c *Client) ListResources() ([]core.ResourceDef, error) {
	result, err := c.Call("resources/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Resources []core.ResourceDef `json:"resources"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.Resources, nil
}

// ListResourceTemplates returns all registered resource templates.
func (c *Client) ListResourceTemplates() ([]core.ResourceTemplate, error) {
	result, err := c.Call("resources/templates/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ResourceTemplates []core.ResourceTemplate `json:"resourceTemplates"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.ResourceTemplates, nil
}

// --- Internal ---

// initializeParams is the params object sent in an initialize request.
type initializeParams struct {
	ProtocolVersion string                   `json:"protocolVersion"`
	Capabilities    core.ClientCapabilities  `json:"capabilities"`
	ClientInfo      core.ClientInfo          `json:"clientInfo"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *core.Error     `json:"error,omitempty"`
}

func (c *Client) nextRequestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) rawCall(method string, params any) (*rpcResponse, error) {
	req := core.Request{
		JSONRPC: "2.0",
		ID:      marshalID(c.nextRequestID()),
		Method:  method,
	}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	data, _ := json.Marshal(req)

	resp, err := c.transport.call(data)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryWithReconnect(func() (*rpcResponse, error) {
			// Re-build with new ID (old may have been consumed)
			req.ID = marshalID(c.nextRequestID())
			data, _ = json.Marshal(req)
			return c.transport.call(data)
		})
	}
	return resp, err
}

// marshalID converts an integer request ID to json.RawMessage.
func marshalID(id int) json.RawMessage {
	raw, _ := json.Marshal(id)
	return raw
}

func (c *Client) notifyMethod(method string, params any) error {
	req := core.Request{
		JSONRPC: "2.0",
		Method:  method,
	}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	data, _ := json.Marshal(req)

	err := c.transport.notify(data)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryNotifyWithReconnect(func() error {
			data, _ = json.Marshal(req)
			return c.transport.notify(data)
		})
	}
	return err
}

// --- Streamable HTTP transport ---

type streamableClientTransport struct {
	url              string
	sessionID        string
	httpClient       *http.Client
	tokenSource      core.TokenSource
	client           *Client                                     // back-pointer for lastEventID tracking
	serverReqHandler func(*core.Request) *core.Response          // set by Client before connect
	notifyHandler    func(method string, params json.RawMessage) // set by Client before connect

	// GET SSE stream state (opt-in via WithGetSSEStream)
	enableGetSSE bool
	getSSEResp   *http.Response       // open GET response (for close)
	getSSEDone   chan struct{}         // closed when background reader exits
	getSSECancel context.CancelFunc   // cancels the GET request context

	// ModifyRequest hook called inside buildReq before auth is applied.
	modifyReq func(*http.Request)
}

func newStreamableClientTransport(url string, ts core.TokenSource) *streamableClientTransport {
	return &streamableClientTransport{url: url, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *streamableClientTransport) connect() error       { return nil }
func (t *streamableClientTransport) getSessionID() string { return t.sessionID }

// close shuts down the transport, including the background GET SSE stream if open.
func (t *streamableClientTransport) close() error {
	if t.getSSECancel != nil {
		t.getSSECancel()
	}
	if t.getSSEResp != nil {
		t.getSSEResp.Body.Close()
	}
	if t.getSSEDone != nil {
		<-t.getSSEDone
	}
	return nil
}

// openGetSSEStream opens a background GET SSE stream on the MCP endpoint for
// receiving server-initiated notifications outside POST request-response cycles.
// Per MCP spec (2025-03-26): "Clients can open an HTTP GET request on the MCP
// endpoint to open an SSE stream." The session ID header attaches the stream
// to the existing session so notifications for this session are delivered here.
func (t *streamableClientTransport) openGetSSEStream() {
	ctx, cancel := context.WithCancel(context.Background())

	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", t.url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		// Send Last-Event-ID for stream resumption if we have one
		if t.client != nil {
			if lastID, ok := t.client.lastEventID.Load().(string); ok && lastID != "" {
				req.Header.Set("Last-Event-ID", lastID)
			}
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		cancel()
		return // non-fatal: client works in POST-only mode
	}

	t.getSSECancel = cancel
	t.getSSEResp = resp
	t.getSSEDone = make(chan struct{})
	go t.backgroundGetReader(resp.Body)
}

// backgroundGetReader reads SSE events from a GET SSE stream and dispatches
// notifications and server-to-client requests to the appropriate handlers.
// Runs until the stream is closed (via close() canceling the context) or
// encounters a read error. Defers closing the done channel to signal shutdown.
func (t *streamableClientTransport) backgroundGetReader(body io.Reader) {
	defer close(t.getSSEDone)
	reader := ssehttp.NewSSEEventReader(body)
	for {
		ev, err := reader.ReadEvent()
		if err != nil {
			return // stream closed or context canceled
		}
		if ev.ID != "" && t.client != nil {
			t.client.lastEventID.Store(ev.ID)
		}
		if ev.Data == "" {
			continue // comment-only or empty event
		}
		t.dispatchSSEEvent(ev.Data)
	}
}

// dispatchSSEEvent parses a single SSE data payload and routes it:
//   - Server-to-client request (method + id): dispatched to serverReqHandler,
//     response POSTed back
//   - Notification (method, no id): delivered to notifyHandler
//   - JSON-RPC response (id, no method): returned for readSSEResponse to use
//
// Shared by both readSSEResponse (POST SSE) and backgroundGetReader (GET SSE)
// to avoid duplicating the probe-and-dispatch logic.
func (t *streamableClientTransport) dispatchSSEEvent(data string) *rpcResponse {
	var probe struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal([]byte(data), &probe) != nil {
		return nil
	}

	// Server-to-client request (has method + id)
	if probe.Method != "" && probe.ID != nil && t.serverReqHandler != nil {
		var req core.Request
		if json.Unmarshal([]byte(data), &req) == nil {
			resp := t.serverReqHandler(&req)
			if resp != nil {
				t.postResponse(resp)
			}
		}
		return nil
	}

	// Notification (method set, no id) — deliver to handler
	if probe.Method != "" && probe.ID == nil {
		if t.notifyHandler != nil {
			var notif struct {
				Params json.RawMessage `json:"params"`
			}
			json.Unmarshal([]byte(data), &notif)
			t.notifyHandler(probe.Method, notif.Params)
		}
		return nil
	}

	// JSON-RPC response (has id, no method) — return for caller to handle
	var resp rpcResponse
	if json.Unmarshal([]byte(data), &resp) == nil && resp.ID != nil {
		return &resp
	}
	return nil
}

func (t *streamableClientTransport) call(data []byte) (*rpcResponse, error) {
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", core.StreamableHTTPAccept)
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Non-2xx responses (401/403 already handled by DoWithAuthRetry).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: strings.TrimSpace(string(body))}
	}

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return t.readSSEResponse(resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result rpcResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %s", string(body))
	}
	return &result, nil
}

// readSSEResponse reads SSE events from a Streamable HTTP response, handling
// server-to-client requests inline and returning the final JSON-RPC response.
// Per MCP spec: "All SSE events that are not JSON-RPC responses or notifications
// SHOULD be ignored." Notifications arrive as intermediate events; the last
// JSON-RPC response with an "id" field is the result.
//
// When a server-to-client request arrives (e.g., sampling/createMessage during
// a tool call), the handler is called and the response is POSTed back to the server.
func (t *streamableClientTransport) readSSEResponse(body io.Reader) (*rpcResponse, error) {
	reader := ssehttp.NewSSEEventReader(body)
	var lastResponse *rpcResponse

	for {
		ev, err := reader.ReadEvent()
		if err != nil {
			// EOF (or EOF-mid-event) — process any final data, then break.
			if ev.Data != "" {
				if resp := t.dispatchSSEEvent(ev.Data); resp != nil {
					lastResponse = resp
				}
			}
			if err == io.EOF {
				break
			}
			if lastResponse != nil {
				return lastResponse, nil
			}
			return nil, fmt.Errorf("reading SSE: %w", err)
		}
		if ev.Data == "" {
			continue
		}
		if resp := t.dispatchSSEEvent(ev.Data); resp != nil {
			lastResponse = resp
		}
	}

	if lastResponse != nil {
		return lastResponse, nil
	}
	return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
}

// postResponse sends a JSON-RPC response back to the server via POST.
// Used when the client handles a server-to-client request during an SSE stream.
func (t *streamableClientTransport) postResponse(resp *core.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.url, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", core.StreamableHTTPAccept)
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}
	httpResp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return
	}
	httpResp.Body.Close()
}

func (t *streamableClientTransport) notify(data []byte) error {
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", core.StreamableHTTPAccept)
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Non-2xx responses (401/403 already handled by DoWithAuthRetry).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return &HTTPStatusError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: strings.TrimSpace(string(body))}
	}
	return nil
}

// --- SSE transport ---

// sseClientTransport implements the MCP SSE transport (2024-11-05).
// Protocol: GET /sse → SSE stream with "endpoint" event containing POST URL →
// POST JSON-RPC to that URL → read "message" events from SSE for responses.
//
// The transport runs a background reader goroutine that demultiplexes incoming
// SSE events into: (1) responses to pending client requests (routed by ID),
// (2) server-to-client requests (sampling, elicitation) dispatched to a handler.
type sseClientTransport struct {
	sseURL      string
	postURL     string
	sessionID   string
	httpClient  *http.Client
	tokenSource core.TokenSource
	sseResp     *http.Response
	sseReader   *ssehttp.SSEEventReader

	// Background reader state
	pendingCalls     conc.SyncMap[string, chan *rpcResponse]       // requestID → response channel
	serverReqHandler func(*core.Request) *core.Response          // set by Client before connect
	notifyHandler    func(method string, params json.RawMessage) // set by Client before connect
	done             chan struct{}                               // closed when background reader exits
	readerErr        error                                       // last error from background reader

	// ModifyRequest hook called inside buildReq before auth is applied.
	modifyReq func(*http.Request)
}

func newSSEClientTransport(sseURL string, ts core.TokenSource) *sseClientTransport {
	return &sseClientTransport{sseURL: sseURL, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *sseClientTransport) connect() error {
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("GET", t.sseURL, nil)
		if err != nil {
			return nil, err
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return fmt.Errorf("GET %s: %w", t.sseURL, err)
	}

	t.sseResp = resp
	t.sseReader = ssehttp.NewSSEEventReader(resp.Body)

	ev, err := t.readSSEEvent()
	if err != nil {
		resp.Body.Close()
		return fmt.Errorf("reading endpoint event: %w", err)
	}
	if ev.event != "endpoint" {
		resp.Body.Close()
		return fmt.Errorf("expected endpoint event, got %q", ev.event)
	}

	resolved, err := ResolveEndpointURL(t.sseURL, ev.data)
	if err != nil {
		resp.Body.Close()
		return fmt.Errorf("resolving endpoint URL: %w", err)
	}
	t.postURL = resolved

	// Extract sessionId from the resolved URL's query parameters.
	if parsed, parseErr := url.Parse(t.postURL); parseErr == nil {
		t.sessionID = parsed.Query().Get("sessionId")
	}

	// Start background reader to demux SSE events.
	t.done = make(chan struct{})
	go t.backgroundReader()

	return nil
}

// backgroundReader continuously reads SSE events from the stream and routes them:
// - Events with "id" + "result"/"error" (no "method") → response to a pending call
// - Events with "method" field → server-to-client request → dispatch and POST response
// - Everything else → discard (notifications, keepalives, etc.)
func (t *sseClientTransport) backgroundReader() {
	defer close(t.done)
	for {
		ev, err := t.readSSEEvent()
		if err != nil {
			t.readerErr = err
			// Wake up any pending callers
			t.pendingCalls.Range(func(_ string, ch chan *rpcResponse) bool {
				select {
				case ch <- nil:
				default:
				}
				return true
			})
			return
		}
		if ev.event != "message" || ev.data == "" {
			continue
		}

		// Try to determine if this is a response or a server request.
		// Probe the JSON for "method" field presence.
		var probe struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(ev.data), &probe) != nil {
			continue
		}

		if probe.Method != "" {
			if probe.ID != nil {
				// Server-to-client request (has method + id) — dispatch to handler
				if t.serverReqHandler != nil {
					var req core.Request
					if json.Unmarshal([]byte(ev.data), &req) == nil {
						resp := t.serverReqHandler(&req)
						if resp != nil {
							t.postResponse(resp)
						}
					}
				}
			} else {
				// Notification (method, no id) — deliver to handler
				if t.notifyHandler != nil {
					var notif struct {
						Params json.RawMessage `json:"params"`
					}
					json.Unmarshal([]byte(ev.data), &notif)
					t.notifyHandler(probe.Method, notif.Params)
				}
			}
			continue
		}

		// core.Response to a pending call — route by ID
		if probe.ID != nil {
			var resp rpcResponse
			if json.Unmarshal([]byte(ev.data), &resp) == nil {
				idStr := normalizeID(probe.ID)
				if ch, ok := t.pendingCalls.LoadAndDelete(idStr); ok {
					ch <- &resp
				}
			}
		}
	}
}

// postResponse sends a JSON-RPC response back to the server via POST.
// Used when the client handles a server-to-client request (sampling, elicitation).
func (t *sseClientTransport) postResponse(resp *core.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}
	httpResp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return
	}
	httpResp.Body.Close()
}

func (t *sseClientTransport) close() error {
	if t.sseResp != nil {
		t.sseResp.Body.Close()
		t.sseResp = nil
	}
	// Wait for background reader to exit
	if t.done != nil {
		<-t.done
	}
	return nil
}

func (t *sseClientTransport) getSessionID() string { return t.sessionID }

func (t *sseClientTransport) call(data []byte) (*rpcResponse, error) {
	// Check if the background reader is already dead before doing any work.
	select {
	case <-t.done:
		if t.readerErr != nil {
			return nil, fmt.Errorf("SSE stream closed: %w", t.readerErr)
		}
		return nil, fmt.Errorf("SSE stream closed unexpectedly")
	default:
	}

	// Extract request ID from the outgoing data to match the response
	var outgoing struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal(data, &outgoing); err != nil {
		return nil, fmt.Errorf("invalid request data: %w", err)
	}
	idStr := normalizeID(outgoing.ID)

	// Register pending channel before POSTing to avoid race with background reader
	ch := make(chan *rpcResponse, 1)
	t.pendingCalls.Store(idStr, ch)
	defer t.pendingCalls.Delete(idStr)

	// POST the request
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	resp.Body.Close()

	// Wait for EITHER the background reader to deliver the response OR the
	// reader to die. Without the t.done case, a dead reader causes call()
	// to block forever — the old bug.
	select {
	case result := <-ch:
		if result == nil {
			if t.readerErr != nil {
				return nil, fmt.Errorf("SSE stream closed: %w", t.readerErr)
			}
			return nil, fmt.Errorf("SSE stream closed unexpectedly")
		}
		return result, nil
	case <-t.done:
		if t.readerErr != nil {
			return nil, fmt.Errorf("SSE stream closed: %w", t.readerErr)
		}
		return nil, fmt.Errorf("SSE stream closed unexpectedly")
	}
}

func (t *sseClientTransport) notify(data []byte) error {
	// Check if the background reader is already dead before POSTing.
	select {
	case <-t.done:
		if t.readerErr != nil {
			return fmt.Errorf("SSE stream closed: %w", t.readerErr)
		}
		return fmt.Errorf("SSE stream closed unexpectedly")
	default:
	}

	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	defer resp.Body.Close()

	// Non-2xx responses (401/403 already handled by DoWithAuthRetry).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return &HTTPStatusError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: strings.TrimSpace(string(body))}
	}
	return nil
}

type sseClientEvent struct {
	event string
	data  string
	id    string
}

// readSSEEvent reads the next SSE event from the stream using the shared
// SSEEventReader. Skips comment-only and empty events automatically.
func (t *sseClientTransport) readSSEEvent() (sseClientEvent, error) {
	for {
		ev, err := t.sseReader.ReadEvent()
		if err != nil {
			return sseClientEvent{}, fmt.Errorf("reading SSE: %w", err)
		}
		// Skip comment-only events (keepalives) — only return events
		// that have an event type or data payload.
		if ev.Event == "" && ev.Data == "" {
			continue
		}
		return sseClientEvent{event: ev.Event, data: ev.Data, id: ev.ID}, nil
	}
}

// normalizeID converts a JSON-RPC ID (int or string) to a consistent string
// representation for use as a map key.
func normalizeID(id any) string {
	switch v := id.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int(v))
	case json.Number:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ClientAuthError is returned by the client transport when the server rejects
// a request with 401 or 403 and the transport has exhausted its retry budget.
type ClientAuthError = ssehttp.AuthRetryError

// HTTPStatusError is returned when the server responds with a non-2xx HTTP
// status code that is not 401/403 (those are handled by DoWithAuthRetry).
// This allows IsTransientError to classify 5xx responses as retriable.
type HTTPStatusError = ssehttp.HTTPStatusError

// DoWithAuthRetry executes an HTTP request with automatic retry on 401/403.
// Wraps core.TokenSource into servicekit's callback-based auth retry.
//
// On 401: calls ts.Token() to refresh, retries once.
// On 403: parses WWW-Authenticate for required scopes, calls
// core.ScopeAwareTokenSource.TokenForScopes if available, retries once.
func DoWithAuthRetry(
	ts core.TokenSource,
	buildReq func() (*http.Request, error),
	do func(*http.Request) (*http.Response, error),
) (*http.Response, error) {
	if ts == nil {
		return ssehttp.DoWithAuthRetry(nil, buildReq, do)
	}

	cfg := &ssehttp.AuthRetryConfig{
		SetAuth: func(req *http.Request) error {
			token, err := ts.Token()
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
		OnUnauthorized: func(resp *http.Response) error {
			// Token() on a dynamic source will refresh; on a static source
			// it returns the same token and the retry will fail → gives up.
			_, err := ts.Token()
			return err
		},
		OnForbidden: func(resp *http.Response) error {
			wwa := resp.Header.Get("WWW-Authenticate")
			_, scopes, _ := ssehttp.ParseWWWAuthenticate(wwa)
			sats, ok := ts.(core.ScopeAwareTokenSource)
			if !ok || len(scopes) == 0 {
				return fmt.Errorf("insufficient scope (token source does not support step-up)")
			}
			_, err := sats.TokenForScopes(scopes)
			return err
		},
	}

	return ssehttp.DoWithAuthRetry(cfg, buildReq, do)
}

// ResolveEndpointURL resolves an SSE endpoint event URL against the base SSE
// connection URL per RFC 3986. Delegates to servicekit's ResolveURL.
func ResolveEndpointURL(baseSSEURL, endpointRef string) (string, error) {
	return ssehttp.ResolveURL(baseSSEURL, endpointRef)
}

// --- core.Response extraction helpers ---

// extractToolText pulls the first text content from a tools/call result.
func extractToolText(raw json.RawMessage) (string, error) {
	var result core.ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("unexpected result: %w", err)
	}
	if result.IsError {
		if len(result.Content) > 0 {
			return "", fmt.Errorf("tool error: %s", result.Content[0].Text)
		}
		return "", fmt.Errorf("tool error (no content)")
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	return result.Content[0].Text, nil
}

// extractResourceText pulls the first text content from a resources/read result.
func extractResourceText(raw json.RawMessage) (string, error) {
	var result core.ResourceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("unexpected result: %w", err)
	}
	if len(result.Contents) == 0 {
		return "", fmt.Errorf("no contents in resource response")
	}
	return result.Contents[0].Text, nil
}

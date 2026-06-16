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
	// call sends a JSON-RPC request and returns the response. The method
	// argument is the JSON-RPC method name carried in data; the streamable
	// HTTP transport mirrors it onto the SEP-2243 Mcp-Method routing
	// header. Other transports may ignore it.
	call(method string, data []byte) (*rpcResponse, error)
	// callWithContext is like call but accepts a typed CallContext carrying
	// per-call cancellation, an optional notification hook, and (on the
	// streamable HTTP transport) the SEP-2243 Mcp-Name header value via
	// cc.mcpName. Transports that can't honor cc (no per-call notification
	// scoping) MUST still issue the call — the hook is a best-effort
	// enhancement, not a requirement. Pass nil cc to behave identically
	// to call().
	callWithContext(method string, data []byte, cc *CallContext) (*rpcResponse, error)
	// notify sends a JSON-RPC notification (no response expected). The
	// method argument is the JSON-RPC method name carried in data; the
	// streamable HTTP transport mirrors it onto the SEP-2243 Mcp-Method
	// routing header. Other transports may ignore it.
	notify(method string, data []byte) error
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
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
type SamplingHandler func(context.Context, core.CreateMessageRequest) (core.CreateMessageResult, error)

// ElicitationHandler handles a server-to-client elicitation/create request.
// The client prompts the user for input and returns the result.
// For URL-mode requests (Mode == "url"), the handler should present the URL
// to the user and return once acknowledged. The actual completion is signaled
// separately via notifications/elicitation/complete.
type ElicitationHandler func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error)

// ElicitationCompleteHandler handles a notifications/elicitation/complete notification.
// Called when the server signals that an out-of-band URL-mode elicitation
// flow has been completed. The client can use this to retry the original request.
type ElicitationCompleteHandler func(context.Context, core.ElicitationCompleteParams)

// RootsHandler handles a server-to-client roots/list request.
// The client returns its current filesystem roots.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
type RootsHandler func(context.Context) ([]core.Root, error)

// WithSamplingHandler registers a handler for server-to-client sampling requests.
// When set, the client advertises the "sampling" capability during initialization.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func WithSamplingHandler(h SamplingHandler) ClientOption {
	return func(c *Client) { c.samplingHandler = h }
}

// WithElicitationHandler registers a handler for server-to-client elicitation requests.
// When set, the client advertises form-mode elicitation capability during initialization.
// To also support URL-mode elicitation, combine with WithElicitationURLSupport.
func WithElicitationHandler(h ElicitationHandler) ClientOption {
	return func(c *Client) { c.elicitationHandler = h }
}

// WithElicitationURLSupport enables URL-mode elicitation capability.
// The same ElicitationHandler receives both form and URL mode requests;
// it should branch on req.Mode. Must be combined with WithElicitationHandler.
func WithElicitationURLSupport() ClientOption {
	return func(c *Client) { c.elicitationURLSupport = true }
}

// WithElicitationCompleteHandler registers a handler for
// notifications/elicitation/complete notifications (SEP-1036).
func WithElicitationCompleteHandler(h ElicitationCompleteHandler) ClientOption {
	return func(c *Client) { c.elicitationCompleteHandler = h }
}

// WithRootsHandler registers a handler for server-to-client roots/list requests.
// When set, the client advertises the "roots" capability (with listChanged: true)
// during initialization, enabling the server to fetch the client's filesystem roots
// after receiving a notifications/roots/list_changed notification.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func WithRootsHandler(h RootsHandler) ClientOption {
	return func(c *Client) { c.rootsHandler = h }
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

// WithInspectResponse sets a callback that is invoked with every HTTP
// response the transport receives. Mirror of WithModifyRequest for the
// inbound direction.
//
// Use it to observe response headers and status — the callback must NOT
// read or close the response body, which the transport owns. Typical
// uses: surfacing custom deployment headers, capturing rate-limit hints,
// reading tracing IDs the server stamps on responses.
//
// Fires on every HTTP response the transport's http.Client returns,
// including pre-retry 401/403 responses that DoWithAuthRetry will retry
// and any non-2xx final response. Only network-level errors (no response
// received) skip the callback.
//
// Only applies to HTTP transports (Streamable HTTP, SSE); ignored for
// stdio and in-process transports.
//
// Example (logging an arbitrary deployment header):
//
//	c := client.NewClient(url, info,
//	    client.WithInspectResponse(func(resp *http.Response) {
//	        if v := resp.Header.Get("X-Some-Deployment-Header"); v != "" {
//	            log.Printf("response header: %s", v)
//	        }
//	    }),
//	)
func WithInspectResponse(fn func(*http.Response)) ClientOption {
	return func(c *Client) { c.inspectResponse = fn }
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

// WithTasksExtension advertises SEP-2663 Tasks support
// (io.modelcontextprotocol/tasks). v2 servers gate task creation and the
// tasks/* methods on this declaration — clients that omit it see synchronous
// tools/call responses and -32601 for tasks/get / tasks/cancel / tasks/update.
func WithTasksExtension() ClientOption {
	return WithExtension(core.TasksExtensionID, core.ClientExtensionCap{})
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

func (a *coreTransportAdapter) call(method string, data []byte) (*rpcResponse, error) {
	return a.callWithContext(method, data, nil)
}

// callWithContext on the core.Transport adapter ignores method and cc —
// core.Transport has no notion of per-call notification scoping, and the
// underlying transport carries its own method routing inside the JSON-RPC
// envelope. Notifications still flow through whatever notify path the
// underlying transport provides.
func (a *coreTransportAdapter) callWithContext(_ string, data []byte, _ *CallContext) (*rpcResponse, error) {
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
	// Convert core.Response (Result any) to rpcResponse (Result json.RawMessage).
	var rawResult json.RawMessage
	if resp.Result != nil {
		if raw, ok := resp.Result.(json.RawMessage); ok {
			rawResult = raw
		} else {
			rawResult, _ = core.MarshalJSON(resp.Result)
		}
	}
	return &rpcResponse{
		JSONRPC: resp.JSONRPC,
		ID:      resp.ID,
		Result:  rawResult,
		Error:   resp.Error,
	}, nil
}

func (a *coreTransportAdapter) notify(_ string, data []byte) error {
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
	samplingHandler              SamplingHandler
	elicitationHandler           ElicitationHandler
	elicitationURLSupport        bool
	fileInputs                   bool
	elicitationCompleteHandler   ElicitationCompleteHandler
	rootsHandler                 RootsHandler

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

	// toolSchemas caches inputSchemas from tools/list responses, keyed by
	// tool name. Populated by ListTools (and refreshed on each call). Used
	// by ToolCall to extract SEP-2243 x-mcp-header annotations without an
	// extra round-trip. Guarded by toolSchemasMu.
	toolSchemas   map[string]core.ToolDef
	toolSchemasMu sync.RWMutex

	// Stdio transport fields (set by WithStdioTransport).
	stdioReader io.Reader
	stdioWriter io.Writer

	// ModifyRequest hook for outgoing HTTP requests (set by WithModifyRequest).
	modifyRequest func(*http.Request)

	// InspectResponse hook fired on every HTTP response the transport
	// receives (set by WithInspectResponse). Headers/status only — must
	// NOT touch the body, which the transport owns.
	inspectResponse func(*http.Response)

	// Call-level middleware chain (set by WithClientMiddleware).
	callMiddleware []ClientMiddleware

	// tracerProvider drives the SEP-414 P3 client-side span emission +
	// W3C _meta.traceparent propagation. Set via WithTracerProvider;
	// nil and core.NoopTracerProvider both skip the install entirely
	// (zero overhead on the unconfigured path).
	tracerProvider core.TracerProvider

	// Command transport fields (set by WithCommandTransport).
	commandName string
	commandArgs []string
	commandOpts []CommandOption

	// connectTimeout limits how long Connect() waits for the transport to
	// become ready (process start + initial handshake). Zero means no timeout.
	// Particularly important for CommandTransport where a misconfigured
	// subprocess may start but never speak the expected protocol.
	connectTimeout time.Duration

	// SEP-2575 wire-mode selection. Seeded by NewClient from
	// ResolveClientMode (option > env > package default). Adaptive
	// is the shipping default. See client/stateless_mode.go.
	mode ClientMode

	// useStatelessWire is set during Connect after wire negotiation:
	// true when the server speaks the SEP-2575 stateless wire (either
	// because ClientModeStateless was pinned OR Adaptive discovered it
	// via server/discover); false when this connection is on the legacy
	// session wire. Once set it does not change for this Client's lifetime.
	useStatelessWire bool

	// negotiatedVersion is the protocol version this client emits in the
	// SEP-2575 _meta envelope and the MCP-Protocol-Version HTTP header on
	// every stateless-wire request. Initialized to
	// core.DraftProtocolVersion2026V1 in NewClient and updated when a
	// server rejects a request with -32001/-32004 + data.supported and
	// the intersection with [core.SupportedStatelessVersions] yields a
	// usable downgrade. Guarded by negotiatedVersionMu — reads happen on
	// every request, writes only on retry, so RWMutex is the right shape.
	negotiatedVersion   string
	negotiatedVersionMu sync.RWMutex
}

// NewClient creates a new MCP client targeting the given server URL.
// By default uses Streamable HTTP. Use WithSSEClient() for SSE transport.
// Call Connect() to perform the protocol handshake.
func NewClient(url string, info core.ClientInfo, opts ...ClientOption) *Client {
	c := &Client{
		url:    url,
		info:   info,
		nextID: 1,
		// SEP-2575 wire mode seeded from env/default; WithClientMode
		// option (if passed) clobbers via the loop below.
		mode:              ResolveClientMode(),
		negotiatedVersion: core.DraftProtocolVersion2026V1,
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
			if c.needsNotifyAdapter() {
				adapter := c.makeNotifyAdapter()
				ct.notifyHandler = func(method string, params []byte) {
					adapter(method, json.RawMessage(params))
				}
			}
			c.transport = &coreTransportAdapter{inner: ct}
		} else if c.stdioReader != nil && c.stdioWriter != nil {
			st := NewStdioTransport(c.stdioReader, c.stdioWriter)
			st.serverReqHandler = func(_ context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}
			if c.needsNotifyAdapter() {
				adapter := c.makeNotifyAdapter()
				st.notifyHandler = func(method string, params []byte) {
					adapter(method, json.RawMessage(params))
				}
			}
			c.transport = &coreTransportAdapter{inner: st}
		} else if c.useSSE {
			st := newSSEClientTransport(c.url, c.tokenSource)
			st.client = c
			st.serverReqHandler = c.HandleServerRequest
			st.modifyReq = c.modifyRequest
			st.inspectResp = c.inspectResponse
			if c.needsNotifyAdapter() {
				st.notifyHandler = c.makeNotifyAdapter()
			}
			c.transport = st
		} else {
			st := newStreamableClientTransport(c.url, c.tokenSource)
			st.client = c
			st.serverReqHandler = c.HandleServerRequest
			st.enableGetSSE = c.enableGetSSE
			st.modifyReq = c.modifyRequest
			st.inspectResp = c.inspectResponse
			if c.needsNotifyAdapter() {
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

	// SEP-2575 wire-mode branching. Three paths:
	//   ClientModeStateless → skip legacy initialize; mark stateless.
	//   ClientModeAdaptive  → probe server/discover first; fall back
	//                         to legacy initialize on -32601/404.
	//   ClientModeLegacyOnly→ original behavior, drop through.
	if c.mode == ClientModeStateless {
		c.useStatelessWire = true
		// Populate ServerInfo via discover so callers don't see a
		// zero ServerInfo struct post-Connect. Failure here is fatal
		// for stateless mode — the client explicitly opted in.
		dr, fallback, err := c.adaptiveProbe()
		if err != nil {
			return fmt.Errorf("stateless mode: server/discover failed: %w", err)
		}
		if fallback {
			return fmt.Errorf("stateless mode: server does not implement server/discover")
		}
		c.ServerInfo = dr.ServerInfo
		c.captureServerExtensions(dr.Capabilities)
		c.startKeepalive()
		return nil
	}
	if c.mode == ClientModeAdaptive {
		dr, fallback, err := c.adaptiveProbe()
		if err != nil {
			return fmt.Errorf("adaptive probe failed: %w", err)
		}
		if !fallback {
			// Server speaks stateless wire.
			c.useStatelessWire = true
			c.ServerInfo = dr.ServerInfo
			c.captureServerExtensions(dr.Capabilities)
			c.startKeepalive()
			return nil
		}
		// Fallback: continue to the legacy initialize handshake below.
	}

	// Build client capabilities based on registered handlers
	caps := core.ClientCapabilities{}
	if c.samplingHandler != nil {
		caps.Sampling = &struct{}{}
	}
	if c.elicitationHandler != nil {
		cap := &core.ElicitationCap{Form: &core.ElicitationFormCap{}}
		if c.elicitationURLSupport {
			cap.URL = &core.ElicitationURLCap{}
		}
		caps.Elicitation = cap
	}
	if c.rootsHandler != nil {
		caps.Roots = &core.RootsCap{ListChanged: true}
	}
	if c.fileInputs {
		caps.FileInputs = &struct{}{}
	}
	if len(c.extensions) > 0 {
		caps.Extensions = make(map[string]core.ClientExtensionCap, len(c.extensions))
		for id, cap := range c.extensions {
			caps.Extensions[id] = cap
		}
	}

	// Initialize handshake
	resp, err := c.rawCall("initialize", initializeParams{
		ProtocolVersion: "2025-11-25",
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
				resp, err := c.rawCall("ping", nil)
				if err != nil {
					failures++
					if failures >= maxFails {
						// Trigger reconnection or close
						if c.maxRetries > 0 {
							c.transport.close()
						}
						return
					}
				} else if resp != nil && resp.Error != nil && resp.Error.Code == core.ErrCodeMethodNotFound {
					// Method-not-found proves the connection is alive — the server
					// received the ping and responded. Per MCP spec, ping is optional.
					failures = 0
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
	// Stop background goroutines on the token source (e.g., proactive
	// token refresh). Uses io.Closer interface to avoid coupling to
	// specific token source implementations.
	if c.tokenSource != nil {
		if closer, ok := c.tokenSource.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
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

// needsNotifyAdapter returns true if any notification-intercepting handler is set.
func (c *Client) needsNotifyAdapter() bool {
	return c.onNotify != nil || c.onContentChunk != nil || c.elicitationCompleteHandler != nil
}

// makeNotifyAdapter creates a transport-level notification handler that
// unmarshals JSON params and delegates to the client's onNotify callback.
// Also intercepts content chunk and elicitation complete notifications.
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
		// Intercept elicitation complete notifications (SEP-1036).
		if c.elicitationCompleteHandler != nil && method == "notifications/elicitation/complete" {
			var p core.ElicitationCompleteParams
			if json.Unmarshal(params, &p) == nil {
				c.elicitationCompleteHandler(context.Background(), p)
			}
			// Still deliver to generic onNotify below.
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

// HandleServerRequest dispatches an incoming server-to-client JSON-RPC
// request to the appropriate registered handler (sampling, elicitation,
// or roots). Returns a JSON-RPC response to send back to the server.
//
// Uses context.Background(); transports that wish to thread their own
// cancellation context (or callers needing to invoke the dispatch
// synthetically — e.g. SEP-2322 MRTR's CallToolWithInputs feeding
// inputRequests through the same routing logic) should call
// HandleServerRequestWithContext instead.
func (c *Client) HandleServerRequest(req *core.Request) *core.Response {
	return c.HandleServerRequestWithContext(context.Background(), req)
}

// HandleServerRequestWithContext is the context-aware form of
// HandleServerRequest. Callers that have a real context (caller-driven
// cancellation, MRTR loops, future client middleware) thread it through
// here so handlers receive it instead of context.Background.
//
// This is the single source of truth for "given an MCP method name and
// a registered handler, what's the response?" Both the transport's
// incoming-request path AND the SEP-2322 MRTR client-side input
// resolution route through this function.
func (c *Client) HandleServerRequestWithContext(ctx context.Context, req *core.Request) *core.Response {
	// SEP-414 P3 — when a non-Noop TracerProvider is configured, extract
	// `_meta.traceparent` from the inbound request, attach it to ctx via
	// core.WithTraceContext (so handler-internal spans treat it as the
	// parent), and emit a wrap span named after the request method. The
	// Noop / nil default skips all of this entirely.
	if tracingEnabled(c.tracerProvider) {
		var span core.Span
		ctx, span = traceInboundDispatch(c.tracerProvider, ctx, req)
		defer func() { span.End() }()
		resp := c.dispatchServerRequest(ctx, req)
		recordInboundOutcome(span, resp)
		return resp
	}
	return c.dispatchServerRequest(ctx, req)
}

// dispatchServerRequest routes an incoming server-to-client JSON-RPC
// request to the appropriate registered handler. Split out from
// HandleServerRequestWithContext so the SEP-414 P3 wrap (trace span
// emission) can sit around the routing without duplicating the
// method switch.
func (c *Client) dispatchServerRequest(ctx context.Context, req *core.Request) *core.Response {
	switch req.Method {
	case "sampling/createMessage":
		if c.samplingHandler == nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "sampling not supported")
		}
		var params core.CreateMessageRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams, "invalid sampling params: "+err.Error())
		}
		result, err := c.samplingHandler(ctx, params)
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
		// Reject URL mode if client didn't declare URL support.
		if params.Mode == core.ElicitModeURL && !c.elicitationURLSupport {
			return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams, "client does not support URL-mode elicitation")
		}
		result, err := c.elicitationHandler(ctx, params)
		if err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInternal, err.Error())
		}
		// SEP-1034: when the user accepted the elicitation, fill in any
		// schema-declared defaults for keys the handler omitted before
		// forwarding the response. Lets handler authors stay
		// SEP-1034-unaware — they return user input as-is. Defaults only
		// apply on "accept" because the result's Content is undefined for
		// reject/cancel.
		if result.Action == "accept" {
			defaults := extractElicitationDefaults(params.RequestedSchema)
			result.Content = mergeElicitationDefaults(result.Content, defaults)
		}
		return core.NewResponse(req.ID, result)

	case "roots/list":
		if c.rootsHandler == nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "roots not supported")
		}
		roots, err := c.rootsHandler(ctx)
		if err != nil {
			return core.NewErrorResponse(req.ID, core.ErrCodeInternal, err.Error())
		}
		return core.NewResponse(req.ID, core.RootsListResult{Roots: roots})

	default:
		return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "unknown server request: "+req.Method)
	}
}

// NotifyRootsChanged sends a notifications/roots/list_changed notification
// to the server. Call this after the client's filesystem roots have changed
// so the server can re-fetch the current list via a roots/list request.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func (c *Client) NotifyRootsChanged() error {
	return c.notifyMethod("notifications/roots/list_changed", nil)
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

// CallContext is a typed context for a single Client.CallContext invocation
// — the client-side analogue of the typed handler contexts in core/ (per
// constraint C1). Embeds context.Context for cancellation; carries a
// per-call notification hook for long-lived calls that need their own
// notification channel (events/stream and likely future tasks/progress
// streaming RPCs).
//
// Construct via NewCallContext, configure via the chainable With*
// methods, then pass to Client.CallContext.
//
// Goroutine model: the notify hook may be called concurrently with the
// session-global callback (set via WithNotificationCallback). Both fire
// on the same notification — the hook is additive, not a replacement.
type CallContext struct {
	context.Context
	notifyHook func(method string, params json.RawMessage)
	// Headers are additional HTTP headers to attach to the outbound request
	// (Streamable HTTP transport only). Used by ToolCall to inject SEP-2243
	// Mcp-Param-* headers derived from the tool's x-mcp-header inputSchema
	// annotations; other transports ignore. Caller-supplied keys override the
	// transport's defaults (Content-Type, Accept, Mcp-Session-Id) only if the
	// caller deliberately sets them — which they shouldn't.
	Headers map[string]string
	// mcpName carries the SEP-2243 Mcp-Name routing header value for the
	// methods that have one (tools/call → params.name, prompts/get →
	// params.name, resources/read → params.uri). Set by the wrapper that
	// builds the call (ToolCall / PromptGet / ResourceRead); the streamable
	// HTTP transport mirrors it onto the wire header. Unexported because
	// callers shouldn't override the wrapper's value.
	mcpName string
}

// NewCallContext wraps a context.Context as a CallContext for use with
// Client.CallContext. The wrapped context controls cancellation: on
// Streamable HTTP, the underlying http.Request is built with it so
// cancelling cancels the in-flight POST. Required for long-lived calls
// (events/stream) so Stop() actually closes the connection.
func NewCallContext(ctx context.Context) *CallContext {
	return &CallContext{Context: ctx}
}

// WithNotifyHook installs a per-call notification hook. The hook fires
// for every notification arriving on this call's response stream — for
// Streamable HTTP, that's the POST SSE response stream the call holds
// open. ADDITIVE to WithNotificationCallback (both fire on the same
// notification).
//
// Transport coverage: wired on the Streamable HTTP transport (the path
// used by events/stream). On stdio / SSE / in-memory transports the
// hook is a no-op for now — notifications still flow only via the
// global callback.
func (cc *CallContext) WithNotifyHook(hook func(method string, params json.RawMessage)) *CallContext {
	cc.notifyHook = hook
	return cc
}

// CallContext issues a JSON-RPC call with per-call configuration carried
// on the typed CallContext. Identical to Call when cc has no hooks set
// beyond a plain context.
func (c *Client) CallContext(cc *CallContext, method string, params any) (*CallResult, error) {
	resp, err := c.rawCallWithContext(method, params, cc)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, &RPCError{
			Code:    resp.Error.Code,
			Message: resp.Error.Message,
			Data:    resp.Error.Data,
		}
	}
	return &CallResult{Raw: resp.Result}, nil
}

// Call makes a JSON-RPC call and returns the parsed response.
func (c *Client) Call(method string, params any) (*CallResult, error) {
	return c.callImpl(context.Background(), method, params)
}

// callImpl is the ctx-aware body of Call. Extracted so package-private
// callers (notably CallToolWithInputs for the SEP-414 P6 / issue 682
// MRTR tracelink capture) can thread a ctx carrying
// core.WithCapturedTraceContext through the middleware chain. The
// trace middleware reads the captured-trace-context holder off ctx
// and writes the outbound TraceContext into it, letting the caller
// learn the round's identity for cross-round linking.
//
// Public Call() preserves the existing ctx-free signature by passing
// context.Background() — no behavior change for non-MRTR callers.
func (c *Client) callImpl(ctx context.Context, method string, params any) (*CallResult, error) {
	traceEnabled := tracingEnabled(c.tracerProvider)
	if len(c.callMiddleware) == 0 && !traceEnabled {
		return c.callDirect(method, params)
	}

	// Build the terminal handler.
	terminal := ClientCallFunc(func(_ context.Context, method string, params any) (*CallResult, error) {
		return c.callDirect(method, params)
	})
	// Wrap with user middleware (reverse order: first registered = outermost).
	handler := terminal
	for i := len(c.callMiddleware) - 1; i >= 0; i-- {
		next := handler
		mw := c.callMiddleware[i]
		handler = func(ctx context.Context, method string, params any) (*CallResult, error) {
			return mw(ctx, method, params, next)
		}
	}
	// SEP-414 P3 — install the trace middleware as the OUTERMOST entry so
	// user middleware (auth retry, header injection, custom logging) runs
	// inside the span. Mirrors the server-side P2 outermost install.
	if traceEnabled {
		next := handler
		mw := traceMiddleware(c, c.tracerProvider)
		handler = func(ctx context.Context, method string, params any) (*CallResult, error) {
			return mw(ctx, method, params, next)
		}
	}
	return handler(ctx, method, params)
}

// callDirect is the non-middleware Call path.
func (c *Client) callDirect(method string, params any) (*CallResult, error) {
	resp, err := c.rawCall(method, params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, &RPCError{
			Code:    resp.Error.Code,
			Message: resp.Error.Message,
			Data:    resp.Error.Data,
		}
	}
	return &CallResult{Raw: resp.Result}, nil
}

// RPCError is a JSON-RPC error returned by the server. It preserves the
// error code, message, and optional data field for structured error handling.
//
// Use errors.As to extract it from a Call/ToolCall error:
//
//	var rpcErr *client.RPCError
//	if errors.As(err, &rpcErr) {
//	    fmt.Println(rpcErr.Code, rpcErr.Data)
//	}
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
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
//
// If the tool's inputSchema (cached by a prior ListTools call) carries
// SEP-2243 x-mcp-header annotations on primitive-typed properties, the
// corresponding argument values are mirrored as Mcp-Param-{Name} HTTP
// headers on the outbound tools/call request (in addition to the JSON
// body). Header values are encoded plain-ASCII or =?base64?{...}?= per
// SEP-2243 §value-encoding. Tools without cached schemas or without
// x-mcp-header annotations send no extra headers (identical to pre-
// SEP-2243 behavior). Caller-side note: middleware registered via
// WithCallMiddleware does not run on the header-mirroring path.
func (c *Client) ToolCall(name string, args any) (string, error) {
	params := map[string]any{"name": name, "arguments": args}
	headers := c.buildToolCallHeaders(name, args)
	result, err := c.callForToolCall(params, headers)
	if err != nil {
		return "", err
	}
	return extractToolText(result.Raw)
}

// buildToolCallHeaders extracts SEP-2243 Mcp-Param-* headers for a tool
// call, consulting the schema cache populated by ListTools. Returns nil
// when no schema is cached or when no x-mcp-header annotations apply to
// the provided args, so callers can use the fast (no-header) path.
func (c *Client) buildToolCallHeaders(name string, args any) map[string]string {
	def, ok := c.lookupToolSchema(name)
	if !ok {
		return nil
	}
	mapping := extractMcpParamHeaders(def.InputSchema)
	if len(mapping) == 0 {
		return nil
	}
	argsMap, ok := args.(map[string]any)
	if !ok {
		return nil
	}
	headers := map[string]string{}
	for propName, headerFragment := range mapping {
		v, present := argsMap[propName]
		if !present {
			continue
		}
		encoded, sendHeader := encodeMcpParamHeaderValue(v)
		if !sendHeader {
			continue
		}
		headers[mcpParamHeaderName(headerFragment)] = encoded
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// callForToolCall dispatches a tools/call. With no headers, takes the
// standard middleware-wrapped Call path. With headers, bypasses
// middleware and goes directly via rawCallWithContext so the per-call
// Headers reach the streamable transport. The middleware bypass is a
// known trade-off; SEP-2243 tool calls are typically conformance / wire-
// behavior paths where middleware doesn't apply.
func (c *Client) callForToolCall(params map[string]any, headers map[string]string) (*CallResult, error) {
	if len(headers) == 0 {
		return c.Call("tools/call", params)
	}
	cc := &CallContext{Headers: headers}
	resp, err := c.rawCallWithContext("tools/call", params, cc)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, &RPCError{Code: resp.Error.Code, Message: resp.Error.Message, Data: resp.Error.Data}
	}
	return &CallResult{Raw: resp.Result}, nil
}

// ToolCallFull invokes a tool and returns the complete result including
// IsError, all content blocks, and the raw JSON. Unlike ToolCall, tool-level
// errors (isError: true) are returned in the result, not as Go errors.
// Only transport/protocol failures produce a Go error.
func (c *Client) ToolCallFull(name string, args any) (*core.ToolResult, error) {
	result, err := c.Call("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var toolResult core.ToolResult
	if err := json.Unmarshal(result.Raw, &toolResult); err != nil {
		return nil, fmt.Errorf("unmarshal tool result: %w", err)
	}
	return &toolResult, nil
}

// ReadResource reads a resource by URI and returns the first text content.
func (c *Client) ReadResource(uri string) (string, error) {
	result, err := c.Call("resources/read", map[string]string{"uri": uri})
	if err != nil {
		return "", err
	}
	return extractResourceText(result.Raw)
}

// ReadResourceFull reads a resource by URI and returns the full typed
// result — every content item plus the SEP-2549 ttlMs / cacheScope cache
// hints. The plain ReadResource helper drops that envelope and returns only
// the first text content; callers that want to cache a read response should
// use ReadResourceFull.
func (c *Client) ReadResourceFull(uri string) (*core.ResourceResult, error) {
	result, err := c.Call("resources/read", map[string]string{"uri": uri})
	if err != nil {
		return nil, err
	}
	var out core.ResourceResult
	if err := json.Unmarshal(result.Raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal resource result: %w", err)
	}
	return &out, nil
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

// ListTools returns all registered tool definitions. As a side effect it
// caches each tool's full ToolDef (including inputSchema) keyed by name —
// ToolCall consults this cache to detect SEP-2243 x-mcp-header annotations
// without a second round-trip.
//
// Tools whose inputSchema fails SEP-2243 x-mcp-header validation (empty
// values, non-primitive types, name charset, case-insensitive duplicates)
// are silently filtered out. Spec: "Client MUST keep valid tools while
// excluding invalid ones." The cache and the returned slice are kept
// consistent — invalid tools never become callable through this client.
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
	valid := make([]core.ToolDef, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		if err := validateMcpParamHeaders(t.InputSchema); err != nil {
			continue
		}
		valid = append(valid, t)
	}
	c.cacheToolSchemas(valid)
	return valid, nil
}

// cacheToolSchemas replaces the tool-schema cache with the supplied tools.
// Replace-rather-than-merge so a refreshed tools/list properly reflects
// server-side removals (server sent a tools/listChanged notification and
// we re-listed).
func (c *Client) cacheToolSchemas(tools []core.ToolDef) {
	c.toolSchemasMu.Lock()
	defer c.toolSchemasMu.Unlock()
	c.toolSchemas = make(map[string]core.ToolDef, len(tools))
	for _, t := range tools {
		c.toolSchemas[t.Name] = t
	}
}

// lookupToolSchema returns the cached ToolDef for the given tool name, if
// any. Callers must not retain the returned ToolDef beyond their immediate
// use — the cache may be replaced concurrently.
func (c *Client) lookupToolSchema(name string) (core.ToolDef, bool) {
	c.toolSchemasMu.RLock()
	defer c.toolSchemasMu.RUnlock()
	t, ok := c.toolSchemas[name]
	return t, ok
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

// ServerExtensionCapability returns the cached ExtensionCapability the
// server advertised for id during initialize, or false when the server
// did not declare the extension.
//
// Use this to inspect extension-specific Config settings (e.g., the
// SEP-2640 directoryRead flag). The returned capability is decoded from
// the raw JSON captured at initialize time and reflects whatever the
// server emitted on the wire; callers should treat unknown Config keys
// permissively.
func (c *Client) ServerExtensionCapability(id string) (core.ExtensionCapability, bool) {
	raw, ok := c.serverExtensions[id]
	if !ok {
		return core.ExtensionCapability{}, false
	}
	var cap core.ExtensionCapability
	if err := json.Unmarshal(raw, &cap); err != nil {
		return core.ExtensionCapability{}, false
	}
	return cap, true
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

// SetLogLevel sets the server's minimum log level for this session via
// logging/setLevel. The server will send notifications/message for log
// entries at or above this level. Use "debug" to see all logs.
func (c *Client) SetLogLevel(level string) error {
	_, err := c.Call("logging/setLevel", map[string]string{"level": level})
	return err
}

// ListPrompts returns all registered prompt definitions.
func (c *Client) ListPrompts() ([]core.PromptDef, error) {
	result, err := c.Call("prompts/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Prompts []core.PromptDef `json:"prompts"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.Prompts, nil
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
	return c.rawCallWithContext(method, params, nil)
}

// rawCallWithContext is the underlying call path used by both Call and
// CallContext. cc may be nil for a no-context call (legacy Call path).
func (c *Client) rawCallWithContext(method string, params any, cc *CallContext) (*rpcResponse, error) {
	// SEP-2243 routing-header derivation runs against the caller's original
	// params (pre-stateless-wrap) so the name/uri stays unwrapped. Allocates
	// a CallContext only when needed so legacy callers without per-call
	// state still see nil cc downstream where it matters.
	if name := deriveMcpName(method, params); name != "" {
		if cc == nil {
			cc = &CallContext{}
		}
		if cc.mcpName == "" {
			cc.mcpName = name
		}
	}

	resp, err := c.doRawCall(method, params, cc)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryWithReconnect(func() (*rpcResponse, error) {
			return c.doRawCall(method, params, cc)
		})
	}
	// SEP-2575 §protocol-version-header: on -32001/-32004 + data.supported,
	// downgrade the negotiated version (so subsequent requests stop hitting
	// the same rejection) and retry this call once with a fresh _meta envelope
	// + MCP-Protocol-Version header. Bounded to one attempt — if the retry
	// also fails, return the second response as-is.
	if retry, picked := isUnsupportedVersionError(resp); retry {
		if c.logger != nil {
			c.logger.Printf("[mcpkit] server rejected protocolVersion=%q; downgrading to %q and retrying",
				c.getNegotiatedVersion(), picked)
		}
		c.setNegotiatedVersion(picked)
		return c.doRawCall(method, params, cc)
	}
	return resp, err
}

// doRawCall is the inner stateless-wrap + marshal + transport-call slice
// shared between rawCallWithContext's first attempt, its transient-error
// retry loop, and its SEP-2575 version-downgrade retry. Each call rebuilds
// params (so the _meta envelope picks up any negotiated-version update)
// and mints a fresh JSON-RPC id (servers reject reused ids).
func (c *Client) doRawCall(method string, params any, cc *CallContext) (*rpcResponse, error) {
	wrapped := c.wrapParamsForStatelessWire(params)
	req := core.Request{
		JSONRPC: "2.0",
		ID:      marshalID(c.nextRequestID()),
		Method:  method,
	}
	if wrapped != nil {
		req.Params, _ = json.Marshal(wrapped)
	}
	data, _ := json.Marshal(req)
	return c.transport.callWithContext(method, data, cc)
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

	err := c.transport.notify(method, data)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryNotifyWithReconnect(func() error {
			data, _ = json.Marshal(req)
			return c.transport.notify(method, data)
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

	// InspectResp fires on every HTTP response from httpClient.Do.
	// Headers/status only — must not touch the body.
	inspectResp func(*http.Response)
}

func newStreamableClientTransport(url string, ts core.TokenSource) *streamableClientTransport {
	return &streamableClientTransport{url: url, httpClient: http.DefaultClient, tokenSource: ts}
}

// do wraps httpClient.Do with the inspect-response hook so every Do
// call site doesn't need a per-site nil-check. Always returns the same
// (resp, err) pair the underlying client returns.
func (t *streamableClientTransport) do(req *http.Request) (*http.Response, error) {
	resp, err := t.httpClient.Do(req)
	if err == nil && t.inspectResp != nil {
		t.inspectResp(resp)
	}
	return resp, err
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

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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
	return t.dispatchSSEEventWithHook(data, nil)
}

// dispatchSSEEventWithHook is the implementation. The optional per-call
// hook fires for every notification frame in addition to the session-global
// notifyHandler — used by events/stream's Stream() helper to receive
// notifications/events/* on a per-call channel without polluting the
// global callback.
func (t *streamableClientTransport) dispatchSSEEventWithHook(data string, hook func(method string, params json.RawMessage)) *rpcResponse {
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

	// Notification (method set, no id) — deliver to global handler AND
	// the per-call hook (if any). Both fire on the same payload — the
	// hook is additive, not a replacement.
	if probe.Method != "" && probe.ID == nil {
		var notif struct {
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal([]byte(data), &notif)
		if t.notifyHandler != nil {
			t.notifyHandler(probe.Method, notif.Params)
		}
		if hook != nil {
			hook(probe.Method, notif.Params)
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

func (t *streamableClientTransport) call(method string, data []byte) (*rpcResponse, error) {
	return t.callWithContext(method, data, nil)
}

// callWithContext issues the POST and, if the response is SSE, threads the
// caller's per-call notify hook (cc.notifyHook) into the SSE-frame loop so
// notifications arriving on this call's response stream reach the hook in
// addition to the session-global callback. Used by events/stream's Stream()
// helper for per-stream demuxing without polluting the global callback.
//
// When cc carries a context, the underlying http.Request is built with it
// so cancelling cancels the in-flight POST — required for events/stream's
// Stop() to actually close the connection.
func (t *streamableClientTransport) callWithContext(method string, data []byte, cc *CallContext) (*rpcResponse, error) {
	reqCtx := context.Background()
	if cc != nil && cc.Context != nil {
		reqCtx = cc.Context
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(reqCtx, "POST", t.url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", core.StreamableHTTPAccept)
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		// SEP-2575: clients on the stateless wire MUST send the
		// protocol version on every request so the server can
		// cross-check it against the _meta envelope. The version
		// tracks Client.negotiatedVersion so retry-with-downgrade
		// flows through here automatically.
		if t.client != nil && t.client.useStatelessWire {
			req.Header.Set(core.HTTPProtocolVersionHeader, t.client.getNegotiatedVersion())
		}
		// SEP-2243 standard routing headers: mirror the JSON-RPC method
		// and (when set by callers like ToolCall / ResourceRead /
		// PromptGet) the per-method name onto Mcp-Method / Mcp-Name so
		// proxies and middleware can route without parsing the body.
		setSEP2243RoutingHeaders(req, method, cc)
		// Caller-supplied per-call headers (SEP-2243 Mcp-Param-* mirroring
		// from ToolCall). Applied after the transport defaults so a caller
		// can't accidentally clobber them — Mcp-Param-* names are reserved
		// for the SEP-2243 mechanism.
		if cc != nil {
			for name, value := range cc.Headers {
				req.Header.Set(name, value)
			}
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Non-2xx responses (401/403 already handled by DoWithAuthRetry).
	// SEP-2575 stateless servers return 4xx with a JSON-RPC error body
	// for HeaderMismatch (-32001), MissingRequiredClientCap (-32003),
	// UnsupportedVersion (-32004), method-not-found on removed methods
	// (-32601), etc. Gated on stateless-wire mode AND Content-Type so
	// legacy 4xx/5xx with non-JSON bodies still surface as HTTPStatusError
	// (preserves backward-compat for callers that consume header data
	// like Retry-After).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if t.client != nil && t.client.useStatelessWire &&
			strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			if rpcResp := tryDecodeJSONRPC(body); rpcResp != nil {
				return &rpcResponse{ID: rpcResp.ID, Result: nil, Error: rpcResp.Error}, nil
			}
		}
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: strings.TrimSpace(string(body))}
	}

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		var hook func(method string, params json.RawMessage)
		if cc != nil {
			hook = cc.notifyHook
		}
		return t.readSSEResponseWithHook(resp.Body, hook)
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
	return t.readSSEResponseWithHook(body, nil)
}

// readSSEResponseWithHook is the implementation; the per-call notify hook
// fires (in addition to the session-global notifyHandler) for every
// notification frame on this response stream. Pass nil hook to behave
// identically to readSSEResponse.
func (t *streamableClientTransport) readSSEResponseWithHook(body io.Reader, hook func(method string, params json.RawMessage)) (*rpcResponse, error) {
	reader := ssehttp.NewSSEEventReader(body)
	var lastResponse *rpcResponse
	var lastEventID string
	var retryMs int

	for {
		ev, err := reader.ReadEvent()
		// Track id + retry from every event (even empty-data ones — the
		// SEP-1699 priming event has id+retry but no data). Used for the
		// reconnect path below when the stream closes before producing a
		// JSON-RPC response.
		if ev.ID != "" {
			lastEventID = ev.ID
		}
		if ev.Retry > 0 {
			retryMs = ev.Retry
		}
		if err != nil {
			// EOF (or EOF-mid-event) — process any final data, then break.
			if ev.Data != "" {
				if resp := t.dispatchSSEEventWithHook(ev.Data, hook); resp != nil {
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
		if resp := t.dispatchSSEEventWithHook(ev.Data, hook); resp != nil {
			lastResponse = resp
		}
	}

	if lastResponse != nil {
		return lastResponse, nil
	}

	// Stream closed gracefully without delivering a JSON-RPC response. Per
	// SEP-1699 (https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1699),
	// the client MUST treat this as a reconnect opportunity: wait the
	// server-supplied retry interval, then issue a GET to the same MCP
	// endpoint with Last-Event-ID set. The response is expected to arrive
	// on the GET SSE stream. If we don't have a Last-Event-ID to resume
	// from, fall back to the legacy error path — without an id the server
	// has no way to replay missed events to us.
	if lastEventID == "" {
		return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
	}
	return t.resumeViaGET(lastEventID, retryMs, hook)
}

// resumeViaGET implements the SEP-1699 client-side reconnect after a
// graceful POST SSE close. It waits retryMs (defaulting to 1000 ms if the
// server didn't set a retry field), then issues a GET to the MCP endpoint
// carrying Last-Event-ID. The response is expected to arrive on the
// resulting SSE stream. Server-to-client requests (sampling/elicitation)
// continue to thread through the per-call notify hook.
func (t *streamableClientTransport) resumeViaGET(lastEventID string, retryMs int, hook func(method string, params json.RawMessage)) (*rpcResponse, error) {
	if retryMs <= 0 {
		retryMs = 1000
	}
	time.Sleep(time.Duration(retryMs) * time.Millisecond)

	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("GET", t.url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Last-Event-ID", lastEventID)
		if t.sessionID != "" {
			req.Header.Set("Mcp-Session-Id", t.sessionID)
		}
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
	if err != nil {
		return nil, fmt.Errorf("SEP-1699 resume GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: strings.TrimSpace(string(body))}
	}

	reader := ssehttp.NewSSEEventReader(resp.Body)
	for {
		ev, err := reader.ReadEvent()
		if err != nil {
			if ev.Data != "" {
				if r := t.dispatchSSEEventWithHook(ev.Data, hook); r != nil {
					return r, nil
				}
			}
			if err == io.EOF {
				return nil, fmt.Errorf("SEP-1699 resume GET closed without response")
			}
			return nil, fmt.Errorf("reading resume GET SSE: %w", err)
		}
		if ev.Data == "" {
			continue
		}
		if r := t.dispatchSSEEventWithHook(ev.Data, hook); r != nil {
			return r, nil
		}
	}
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
	httpResp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
	if err != nil {
		return
	}
	httpResp.Body.Close()
}

func (t *streamableClientTransport) notify(method string, data []byte) error {
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
		// SEP-2243 routing headers — notifications never carry an Mcp-Name
		// (no params.name/uri), so cc is nil here on purpose.
		setSEP2243RoutingHeaders(req, method, nil)
		if t.modifyReq != nil {
			t.modifyReq(req)
		}
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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

	// Back-pointer to the owning Client for lastEventID tracking.
	// Set during Connect() and reconnect().
	client *Client

	// Background reader state
	pendingCalls     conc.SyncMap[string, chan *rpcResponse]       // requestID → response channel
	serverReqHandler func(*core.Request) *core.Response          // set by Client before connect
	notifyHandler    func(method string, params json.RawMessage) // set by Client before connect
	done             chan struct{}                               // closed when background reader exits
	readerErr        error                                       // last error from background reader

	// ModifyRequest hook called inside buildReq before auth is applied.
	modifyReq func(*http.Request)

	// InspectResp fires on every HTTP response from httpClient.Do.
	// Headers/status only — must not touch the body.
	inspectResp func(*http.Response)
}

func newSSEClientTransport(sseURL string, ts core.TokenSource) *sseClientTransport {
	return &sseClientTransport{sseURL: sseURL, httpClient: http.DefaultClient, tokenSource: ts}
}

// do wraps httpClient.Do with the inspect-response hook so every Do
// call site doesn't need a per-site nil-check.
func (t *sseClientTransport) do(req *http.Request) (*http.Response, error) {
	resp, err := t.httpClient.Do(req)
	if err == nil && t.inspectResp != nil {
		t.inspectResp(resp)
	}
	return resp, err
}

func (t *sseClientTransport) connect() error {
	buildReq := func() (*http.Request, error) {
		// On reconnect, include session ID so the server can resume
		// the existing session within its grace period.
		sseURL := t.sseURL
		if t.sessionID != "" {
			sep := "?"
			if strings.Contains(sseURL, "?") {
				sep = "&"
			}
			sseURL += sep + "sessionId=" + t.sessionID
		}
		req, err := http.NewRequest("GET", sseURL, nil)
		if err != nil {
			return nil, err
		}
		// Send Last-Event-ID for stream resumption — the server replays
		// missed events from its EventStore before starting live delivery.
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

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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
		// Track the last event ID for stream resumption on reconnect.
		if ev.id != "" && t.client != nil {
			t.client.lastEventID.Store(ev.id)
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
	httpResp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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

// callWithContext on the legacy SSE transport ignores method and cc —
// notifications arrive on the GET stream (background reader), not on a
// per-call response, so per-call scoping is not meaningful here. The SEP-2243
// routing headers are also a no-op on this transport (legacy SSE predates
// the SEP). Notifications still reach the session-global callback via the
// existing path.
func (t *sseClientTransport) callWithContext(method string, data []byte, _ *CallContext) (*rpcResponse, error) {
	return t.call(method, data)
}

func (t *sseClientTransport) call(_ string, data []byte) (*rpcResponse, error) {
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

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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

func (t *sseClientTransport) notify(_ string, data []byte) error {
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

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.do)
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
			// If the source supports cache invalidation (e.g.
			// OAuthTokenSource implementing core.InvalidatingTokenSource),
			// drop its cached authInfo/credentials BEFORE calling Token
			// for the retry. Without this, a cached token wins over the
			// authorization server's 401 signal and we'd retry with the
			// same stale credential — which the spec requires for
			// SEP-2352 AS-change re-discovery to actually take effect.
			// Plain sources (static tokens, simple bearers) don't
			// implement the interface; existing behavior unchanged.
			if inv, ok := ts.(core.InvalidatingTokenSource); ok {
				inv.Invalidate()
			}
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

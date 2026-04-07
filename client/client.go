package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	core "github.com/panyam/mcpkit/core"
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
	// Convert core.Response → rpcResponse
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, err
	}
	return &rr, nil
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
	// Currently only used by the in-memory transport.
	onNotify func(method string, params any)
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

// Connect establishes the transport and performs the MCP initialize handshake.
func (c *Client) Connect() error {
	// Create transport (skip if already set, e.g., by WithInMemoryServer)
	if c.transport == nil {
		if c.useSSE {
			st := newSSEClientTransport(c.url, c.tokenSource)
			st.serverReqHandler = c.HandleServerRequest
			if c.onNotify != nil {
				st.notifyHandler = c.makeNotifyAdapter()
			}
			c.transport = st
		} else {
			st := newStreamableClientTransport(c.url, c.tokenSource)
			st.serverReqHandler = c.HandleServerRequest
			if c.onNotify != nil {
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
	caps := map[string]any{}
	if c.samplingHandler != nil {
		caps["sampling"] = map[string]any{}
	}
	if c.elicitationHandler != nil {
		caps["elicitation"] = map[string]any{}
	}
	if len(c.extensions) > 0 {
		exts := make(map[string]any, len(c.extensions))
		for id, cap := range c.extensions {
			exts[id] = cap
		}
		caps["extensions"] = exts
	}

	// Initialize handshake
	resp, err := c.rawCall("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    caps,
		"clientInfo":      c.info,
	})
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	// Extract server info and capabilities from initialize response
	if result, ok := resp.Result.(map[string]any); ok {
		if si, ok := result["serverInfo"].(map[string]any); ok {
			c.ServerInfo.Name, _ = si["name"].(string)
			c.ServerInfo.Version, _ = si["version"].(string)
		}
		// Parse server extensions from capabilities
		if capObj, ok := result["capabilities"].(map[string]any); ok {
			if exts, ok := capObj["extensions"].(map[string]any); ok {
				c.serverExtensions = make(map[string]json.RawMessage, len(exts))
				for id, v := range exts {
					if raw, err := json.Marshal(v); err == nil {
						c.serverExtensions[id] = raw
					}
				}
			}
		}
	}

	// Send initialized notification
	return c.notifyMethod("notifications/initialized", nil)
}

// Close terminates the client session and transport.
func (c *Client) Close() error {
	if c.transport != nil {
		return c.transport.close()
	}
	return nil
}

// makeNotifyAdapter creates a transport-level notification handler that
// unmarshals JSON params and delegates to the client's onNotify callback.
func (c *Client) makeNotifyAdapter() func(string, json.RawMessage) {
	return func(method string, params json.RawMessage) {
		var parsed any
		if len(params) > 0 {
			json.Unmarshal(params, &parsed)
		}
		c.onNotify(method, parsed)
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

// CallResult holds the raw result from a JSON-RPC call.
type CallResult struct {
	Raw any
}

// JSON returns the result as indented JSON.
func (r *CallResult) JSON() string {
	data, _ := json.MarshalIndent(r.Raw, "", "  ")
	return string(data)
}

// Unmarshal decodes the result into the given value.
func (r *CallResult) Unmarshal(v any) error {
	data, err := json.Marshal(r.Raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// --- Convenience methods ---

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

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *core.Error `json:"error,omitempty"`
}

func (c *Client) nextRequestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) rawCall(method string, params any) (*rpcResponse, error) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextRequestID(),
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	data, _ := json.Marshal(reqBody)

	resp, err := c.transport.call(data)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryWithReconnect(func() (*rpcResponse, error) {
			// Re-build with new ID (old may have been consumed)
			reqBody["id"] = c.nextRequestID()
			data, _ = json.Marshal(reqBody)
			return c.transport.call(data)
		})
	}
	return resp, err
}

func (c *Client) notifyMethod(method string, params any) error {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	data, _ := json.Marshal(reqBody)

	err := c.transport.notify(data)
	if err != nil && c.maxRetries > 0 && IsTransientError(err) {
		return c.retryNotifyWithReconnect(func() error {
			data, _ = json.Marshal(reqBody)
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
	serverReqHandler func(*core.Request) *core.Response          // set by Client before connect
	notifyHandler    func(method string, params json.RawMessage) // set by Client before connect
}

func newStreamableClientTransport(url string, ts core.TokenSource) *streamableClientTransport {
	return &streamableClientTransport{url: url, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *streamableClientTransport) connect() error       { return nil }
func (t *streamableClientTransport) close() error         { return nil }
func (t *streamableClientTransport) getSessionID() string { return t.sessionID }

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
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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
	scanner := bufio.NewReader(body)
	var lastResponse *rpcResponse

	for {
		line, err := scanner.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if lastResponse != nil {
				return lastResponse, nil
			}
			return nil, fmt.Errorf("reading SSE: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(line[5:])
			if data == "" {
				continue
			}

			// Probe to distinguish server requests from responses/notifications
			var probe struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(data), &probe) != nil {
				continue
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
				continue
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
				continue
			}

			// JSON-RPC response (has id)
			var resp rpcResponse
			if json.Unmarshal([]byte(data), &resp) == nil && resp.ID != nil {
				lastResponse = &resp
			}
		}
		// Skip event:, id:, comments, blank lines
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
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return err
	}
	resp.Body.Close()
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
	sseReader   *bufio.Reader

	// Background reader state
	pendingCalls     sync.Map                                    // requestID (string) → chan *rpcResponse
	serverReqHandler func(*core.Request) *core.Response          // set by Client before connect
	notifyHandler    func(method string, params json.RawMessage) // set by Client before connect
	done             chan struct{}                               // closed when background reader exits
	readerErr        error                                       // last error from background reader
}

func newSSEClientTransport(sseURL string, ts core.TokenSource) *sseClientTransport {
	return &sseClientTransport{sseURL: sseURL, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *sseClientTransport) connect() error {
	buildReq := func() (*http.Request, error) {
		return http.NewRequest("GET", t.sseURL, nil)
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return fmt.Errorf("GET %s: %w", t.sseURL, err)
	}

	t.sseResp = resp
	t.sseReader = bufio.NewReader(resp.Body)

	ev, err := t.readSSEEvent()
	if err != nil {
		resp.Body.Close()
		return fmt.Errorf("reading endpoint event: %w", err)
	}
	if ev.event != "endpoint" {
		resp.Body.Close()
		return fmt.Errorf("expected endpoint event, got %q", ev.event)
	}

	t.postURL = ev.data

	if idx := strings.Index(t.postURL, "sessionId="); idx >= 0 {
		t.sessionID = t.postURL[idx+len("sessionId="):]
		if amp := strings.Index(t.sessionID, "&"); amp >= 0 {
			t.sessionID = t.sessionID[:amp]
		}
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
			t.pendingCalls.Range(func(key, value any) bool {
				ch := value.(chan *rpcResponse)
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
					ch.(chan *rpcResponse) <- &resp
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
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	resp.Body.Close()

	// Wait for the background reader to deliver the response
	result := <-ch
	if result == nil {
		if t.readerErr != nil {
			return nil, fmt.Errorf("SSE stream closed: %w", t.readerErr)
		}
		return nil, fmt.Errorf("SSE stream closed unexpectedly")
	}
	return result, nil
}

func (t *sseClientTransport) notify(data []byte) error {
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}

	resp, err := DoWithAuthRetry(t.tokenSource, buildReq, t.httpClient.Do)
	if err != nil {
		return fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	resp.Body.Close()
	return nil
}

type sseClientEvent struct {
	event string
	data  string
}

// readSSEEvent reads the next SSE event from the stream, skipping keepalive comments.
func (t *sseClientTransport) readSSEEvent() (sseClientEvent, error) {
	var event, data string
	for {
		line, err := t.sseReader.ReadString('\n')
		if err != nil {
			return sseClientEvent{}, fmt.Errorf("reading SSE: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			if data != "" || event != "" {
				return sseClientEvent{event: event, data: data}, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // keepalive comment
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
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

// setAuthHeader sets the Authorization: Bearer header from a core.TokenSource.
// No-op if ts is nil.
func setAuthHeader(req *http.Request, ts core.TokenSource) error {
	if ts == nil {
		return nil
	}
	token, err := ts.Token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// ClientAuthError is returned by the client transport when the server rejects
// a request with 401 or 403 and the transport has exhausted its retry budget.
type ClientAuthError struct {
	// StatusCode is the HTTP status (401 or 403).
	StatusCode int
	// Message describes the failure.
	Message string
	// WWWAuthenticate is the raw WWW-Authenticate header from the server response.
	WWWAuthenticate string
	// RequiredScopes are the scopes parsed from the WWW-Authenticate header (403 only).
	RequiredScopes []string
}

func (e *ClientAuthError) Error() string {
	return fmt.Sprintf("auth error %d: %s", e.StatusCode, e.Message)
}

// DoWithAuthRetry executes an HTTP request with automatic retry on 401/403.
//
// Retry budget: max 1 retry for 401 (token refresh), max 1 retry for 403
// (scope step-up). Total max 2 retries per request.
//
// On 401: calls core.TokenSource.Token() to get a fresh token, retries once.
// On 403: parses WWW-Authenticate for required scopes, calls
// core.ScopeAwareTokenSource.TokenForScopes if available, retries once.
//
// buildReq must create a new *http.Request each call (body may be consumed).
// do is typically httpClient.Do.
func DoWithAuthRetry(
	ts core.TokenSource,
	buildReq func() (*http.Request, error),
	do func(*http.Request) (*http.Response, error),
) (*http.Response, error) {
	var tried401, tried403 bool

	for {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		if err := setAuthHeader(req, ts); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}

		resp, err := do(req)
		if err != nil {
			return nil, err
		}

		switch resp.StatusCode {
		case http.StatusUnauthorized: // 401
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if tried401 || ts == nil {
				return nil, &ClientAuthError{
					StatusCode:      401,
					Message:         strings.TrimSpace(string(body)),
					WWWAuthenticate: resp.Header.Get("WWW-Authenticate"),
				}
			}
			tried401 = true
			// Token() on a dynamic source will refresh; on a static source
			// it returns the same token and the retry will fail → gives up.
			if _, err := ts.Token(); err != nil {
				return nil, fmt.Errorf("token refresh: %w", err)
			}
			continue

		case http.StatusForbidden: // 403
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			wwa := resp.Header.Get("WWW-Authenticate")
			var scopes []string
			if wwa != "" {
				_, scopes, _ = core.ParseWWWAuthenticate(wwa)
			}
			if tried403 || ts == nil {
				return nil, &ClientAuthError{
					StatusCode:      403,
					Message:         strings.TrimSpace(string(body)),
					WWWAuthenticate: wwa,
					RequiredScopes:  scopes,
				}
			}
			tried403 = true
			sats, ok := ts.(core.ScopeAwareTokenSource)
			if !ok || len(scopes) == 0 {
				return nil, &ClientAuthError{
					StatusCode:      403,
					Message:         "insufficient scope (token source does not support step-up)",
					WWWAuthenticate: wwa,
					RequiredScopes:  scopes,
				}
			}
			if _, err := sats.TokenForScopes(scopes); err != nil {
				return nil, fmt.Errorf("scope step-up: %w", err)
			}
			continue

		default:
			return resp, nil
		}
	}
}

// --- core.Response extraction helpers ---

// extractToolText pulls the first text content from a tools/call result.
func extractToolText(raw any) (string, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected result type: %T", raw)
	}
	if isErr, _ := m["isError"].(bool); isErr {
		content, _ := m["content"].([]any)
		if len(content) > 0 {
			item, _ := content[0].(map[string]any)
			text, _ := item["text"].(string)
			return "", fmt.Errorf("tool error: %s", text)
		}
		return "", fmt.Errorf("tool error (no content)")
	}
	content, _ := m["content"].([]any)
	if len(content) == 0 {
		return "", nil
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	return text, nil
}

// extractResourceText pulls the first text content from a resources/read result.
func extractResourceText(raw any) (string, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected result type: %T", raw)
	}
	contents, _ := m["contents"].([]any)
	if len(contents) == 0 {
		return "", fmt.Errorf("no contents in resource response")
	}
	item, _ := contents[0].(map[string]any)
	text, _ := item["text"].(string)
	return text, nil
}

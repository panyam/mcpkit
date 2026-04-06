package mcpkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
	return func(c *Client) { c.tokenSource = &staticTokenSource{token: token} }
}

// WithTokenSource sets a dynamic token source for all client requests.
// Use this for OAuth flows where tokens are refreshed automatically.
func WithTokenSource(ts TokenSource) ClientOption {
	return func(c *Client) { c.tokenSource = ts }
}

// Client is an MCP client that communicates over Streamable HTTP or SSE.
type Client struct {
	url         string
	info        ClientInfo
	useSSE      bool
	tokenSource TokenSource
	nextID      int
	mu          sync.Mutex
	transport   clientTransport

	// ServerInfo is populated after Connect.
	ServerInfo ServerInfo
}

// NewClient creates a new MCP client targeting the given server URL.
// By default uses Streamable HTTP. Use WithSSEClient() for SSE transport.
// Call Connect() to perform the protocol handshake.
func NewClient(url string, info ClientInfo, opts ...ClientOption) *Client {
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
	// Create transport
	if c.useSSE {
		c.transport = newSSEClientTransport(c.url, c.tokenSource)
	} else {
		c.transport = newStreamableClientTransport(c.url, c.tokenSource)
	}

	if err := c.transport.connect(); err != nil {
		return fmt.Errorf("transport connect: %w", err)
	}

	// Initialize handshake
	resp, err := c.rawCall("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      c.info,
	})
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	// Extract server info
	if result, ok := resp.Result.(map[string]any); ok {
		if si, ok := result["serverInfo"].(map[string]any); ok {
			c.ServerInfo.Name, _ = si["name"].(string)
			c.ServerInfo.Version, _ = si["version"].(string)
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

// SessionID returns the current session ID.
func (c *Client) SessionID() string {
	if st, ok := c.transport.(*streamableClientTransport); ok {
		return st.sessionID
	}
	if sse, ok := c.transport.(*sseClientTransport); ok {
		return sse.sessionID
	}
	return ""
}

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

// ListTools returns all registered tool definitions.
func (c *Client) ListTools() ([]ToolDef, error) {
	result, err := c.Call("tools/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

// ListResources returns all registered static resources.
func (c *Client) ListResources() ([]ResourceDef, error) {
	result, err := c.Call("resources/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Resources []ResourceDef `json:"resources"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.Resources, nil
}

// ListResourceTemplates returns all registered resource templates.
func (c *Client) ListResourceTemplates() ([]ResourceTemplate, error) {
	result, err := c.Call("resources/templates/list", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	}
	if err := result.Unmarshal(&resp); err != nil {
		return nil, err
	}
	return resp.ResourceTemplates, nil
}

// --- Internal ---

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
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
	return c.transport.call(data)
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
	return c.transport.notify(data)
}

// --- Streamable HTTP transport ---

type streamableClientTransport struct {
	url         string
	sessionID   string
	httpClient  *http.Client
	tokenSource TokenSource
}

func newStreamableClientTransport(url string, ts TokenSource) *streamableClientTransport {
	return &streamableClientTransport{url: url, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *streamableClientTransport) connect() error { return nil }
func (t *streamableClientTransport) close() error   { return nil }

func (t *streamableClientTransport) call(data []byte) (*rpcResponse, error) {
	req, err := http.NewRequest("POST", t.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Per MCP spec (2025-11-25): clients MUST accept both application/json and text/event-stream
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	req.Header.Set("Accept", StreamableHTTPAccept)
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if err := setAuthHeader(req, t.tokenSource); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}

	// Per MCP spec (2025-11-25): server returns either application/json or text/event-stream.
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSEResponse(resp.Body)
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

// readSSEResponse reads SSE events from a Streamable HTTP response, discarding
// notification events and returning the final JSON-RPC response.
// Per MCP spec: "All SSE events that are not JSON-RPC responses or notifications
// SHOULD be ignored." Notifications arrive as intermediate events; the last
// JSON-RPC response with an "id" field is the result.
func readSSEResponse(body io.Reader) (*rpcResponse, error) {
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
			var resp rpcResponse
			if json.Unmarshal([]byte(data), &resp) == nil {
				if resp.ID != nil {
					// This is the JSON-RPC response (has an id)
					lastResponse = &resp
				}
				// else: notification (no id) — discard
			}
		}
		// Skip event:, id:, comments, blank lines
	}

	if lastResponse != nil {
		return lastResponse, nil
	}
	return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
}

func (t *streamableClientTransport) notify(data []byte) error {
	req, err := http.NewRequest("POST", t.url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", StreamableHTTPAccept)
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if err := setAuthHeader(req, t.tokenSource); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	resp, err := t.httpClient.Do(req)
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
type sseClientTransport struct {
	sseURL      string
	postURL     string
	sessionID   string
	httpClient  *http.Client
	tokenSource TokenSource
	sseResp     *http.Response
	sseReader   *bufio.Reader
	mu          sync.Mutex
}

func newSSEClientTransport(sseURL string, ts TokenSource) *sseClientTransport {
	return &sseClientTransport{sseURL: sseURL, httpClient: http.DefaultClient, tokenSource: ts}
}

func (t *sseClientTransport) connect() error {
	// Open SSE stream with auth
	req, err := http.NewRequest("GET", t.sseURL, nil)
	if err != nil {
		return err
	}
	if err := setAuthHeader(req, t.tokenSource); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", t.sseURL, err)
	}

	t.sseResp = resp
	t.sseReader = bufio.NewReader(resp.Body)

	// Read the endpoint event
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

	// Extract sessionId from POST URL
	if idx := strings.Index(t.postURL, "sessionId="); idx >= 0 {
		t.sessionID = t.postURL[idx+len("sessionId="):]
		if amp := strings.Index(t.sessionID, "&"); amp >= 0 {
			t.sessionID = t.sessionID[:amp]
		}
	}

	return nil
}

func (t *sseClientTransport) close() error {
	if t.sseResp != nil {
		t.sseResp.Body.Close()
		t.sseResp = nil
	}
	return nil
}

func (t *sseClientTransport) call(data []byte) (*rpcResponse, error) {
	// POST the request with auth
	req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := setAuthHeader(req, t.tokenSource); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	resp.Body.Close()

	// Read the response from the SSE stream
	t.mu.Lock()
	defer t.mu.Unlock()

	ev, err := t.readSSEEvent()
	if err != nil {
		return nil, fmt.Errorf("reading SSE response: %w", err)
	}
	if ev.event != "message" {
		return nil, fmt.Errorf("expected message event, got %q", ev.event)
	}

	var result rpcResponse
	if err := json.Unmarshal([]byte(ev.data), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON in SSE message: %s", ev.data)
	}
	return &result, nil
}

func (t *sseClientTransport) notify(data []byte) error {
	req, err := http.NewRequest("POST", t.postURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", t.postURL, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := setAuthHeader(req, t.tokenSource); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	resp, err := t.httpClient.Do(req)
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

// setAuthHeader sets the Authorization: Bearer header from a TokenSource.
// No-op if ts is nil.
func setAuthHeader(req *http.Request, ts TokenSource) error {
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

// --- Response extraction helpers ---

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

package mcpkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Client is an MCP client that communicates over Streamable HTTP.
// Use ClientInfo (defined in dispatch.go) for initialization.
type Client struct {
	url        string
	info       ClientInfo
	sessionID  string
	nextID     int
	mu         sync.Mutex
	httpClient *http.Client

	// ServerInfo is populated after Connect.
	ServerInfo ServerInfo
}

// NewClient creates a new MCP client targeting the given server URL.
// Call Connect() to perform the protocol handshake.
func NewClient(url string, info ClientInfo) *Client {
	return &Client{
		url:        url,
		info:       info,
		nextID:     1,
		httpClient: http.DefaultClient,
	}
}

// Connect performs the MCP initialize handshake and stores the session ID.
func (c *Client) Connect() error {
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
	return c.notify("notifications/initialized", nil)
}

// Close terminates the client session.
func (c *Client) Close() error {
	c.sessionID = ""
	return nil
}

// SessionID returns the current session ID.
func (c *Client) SessionID() string {
	return c.sessionID
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
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *Error         `json:"error,omitempty"`
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

	req, err := http.NewRequest("POST", c.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture session ID from response headers
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
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

func (c *Client) notify(method string, params any) error {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

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

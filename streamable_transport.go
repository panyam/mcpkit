package mcpkit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	// mcpSessionIDHeader is the HTTP header for session identity per MCP Streamable HTTP spec.
	mcpSessionIDHeader = "Mcp-Session-Id"

	// mcpProtocolVersionHeader is the HTTP header for protocol version per MCP spec.
	mcpProtocolVersionHeader = "MCP-Protocol-Version"

	// StreamableHTTPAccept is the required Accept header value for Streamable HTTP requests.
	// Per MCP spec (2025-11-25, Streamable HTTP transport): clients MUST accept both
	// application/json and text/event-stream.
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	StreamableHTTPAccept = "application/json, text/event-stream"
)

// streamableTransport implements the MCP Streamable HTTP transport (2025-03-26 spec).
// Each session is tracked via the Mcp-Session-Id header. Sessions are created on
// initialize and cleaned up via DELETE or server restart.
//
// Unlike the SSE transport, there are no long-lived connections — each request is
// independent HTTP with the response returned directly in the body. Sessions are
// lightweight map entries (a Dispatcher with negotiation state), so abandoned
// sessions have minimal cost.
type streamableTransport struct {
	server   *Server
	sessions sync.Map // sessionID → *Dispatcher
	config   transportConfig
}

// newStreamableTransport creates a Streamable HTTP transport.
func newStreamableTransport(s *Server, cfg transportConfig) *streamableTransport {
	return &streamableTransport{
		server: s,
		config: cfg,
	}
}

// handler returns an http.Handler that serves the Streamable HTTP endpoint.
func (t *streamableTransport) handler() http.Handler {
	mux := http.NewServeMux()
	prefix := strings.TrimRight(t.config.prefix, "/")
	mux.HandleFunc(prefix, t.handleRoot)
	return mux
}

// handleRoot routes requests by HTTP method at the base prefix.
// Validates Origin/Host headers to prevent DNS rebinding attacks per MCP spec.
func (t *streamableTransport) handleRoot(w http.ResponseWriter, r *http.Request) {
	if !t.validateOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodPost:
		t.handlePost(w, r)
	case http.MethodDelete:
		t.handleDelete(w, r)
	case http.MethodGet:
		// Future: SSE stream for server-initiated notifications.
		http.Error(w, "GET SSE stream not yet supported", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost handles POST requests: JSON-RPC dispatch with session management.
func (t *streamableTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	// NOTE: Per MCP spec (2025-11-25, Streamable HTTP transport), clients MUST include
	// Accept header that accepts both application/json and text/event-stream.
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	// We validate this client-side (StreamableHTTPAccept constant) but do NOT reject
	// non-conforming requests server-side — the spec places the MUST on the client,
	// and rejecting would break backward compatibility with older clients.

	// Auth check
	claims, err := t.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	// Read and parse JSON body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		// Parse error → JSON-RPC error in response body
		w.Header().Set("Content-Type", "application/json")
		errResp := NewErrorResponse(json.RawMessage("null"), ErrCodeParse, "parse error: "+err.Error())
		raw, _ := json.Marshal(errResp)
		w.Write(raw)
		return
	}

	// Route: initialize creates a new session; everything else requires one
	if req.Method == "initialize" {
		t.handleInitialize(w, r, claims, &req)
		return
	}

	// Non-initialize: require session
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		http.Error(w, "missing "+mcpSessionIDHeader+" header", http.StatusBadRequest)
		return
	}

	dispVal, ok := t.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	dispatcher := dispVal.(*Dispatcher)

	// Validate MCP-Protocol-Version if present
	if protoVer := r.Header.Get(mcpProtocolVersionHeader); protoVer != "" {
		if negotiated := dispatcher.NegotiatedVersion(); negotiated != "" && protoVer != negotiated {
			http.Error(w, "protocol version mismatch", http.StatusBadRequest)
			return
		}
	}

	if shouldStreamSSE(r.Header.Get("Accept"), &req) {
		t.handlePostSSE(w, r, claims, dispatcher, &req)
		return
	}

	// Synchronous JSON path (no mid-request notifications)
	resp := t.server.dispatchWith(dispatcher, r.Context(), claims, &req)

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	raw, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Write(raw)
}

// handlePostSSE handles a POST request using SSE streaming, allowing the server
// to send notifications (logging, progress) as SSE events before the final
// JSON-RPC response. Per MCP spec: "the server MUST either return
// Content-Type: text/event-stream, to initiate an SSE stream, or
// Content-Type: application/json, to return one JSON object."
func (t *streamableTransport) handlePostSSE(w http.ResponseWriter, r *http.Request, claims *Claims, d *Dispatcher, req *Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fall back to synchronous JSON if flushing not supported
		resp := t.server.dispatchWith(d, r.Context(), claims, req)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(resp)
		w.Write(raw)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var mu sync.Mutex
	writeSSE := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
	}

	// Wire notifyFunc to SSE writer for this request.
	// This enables EmitLog() and Notify() calls in tool handlers to push
	// notifications as SSE events during request execution.
	prevNotify := d.notifyFunc
	d.notifyFunc = func(method string, params any) {
		raw, err := marshalNotification(method, params)
		if err != nil {
			return
		}
		writeSSE(raw)
	}
	defer func() { d.notifyFunc = prevNotify }()

	// Dispatch (synchronous — notifications stream as events during execution)
	resp := t.server.dispatchWith(d, r.Context(), claims, req)

	// Write the JSON-RPC response as the final SSE event
	if resp != nil {
		raw, _ := json.Marshal(resp)
		writeSSE(raw)
	}
}

// handleInitialize handles POST initialize: creates session, dispatches, returns
// the response with Mcp-Session-Id header.
func (t *streamableTransport) handleInitialize(w http.ResponseWriter, r *http.Request, claims *Claims, req *Request) {
	// Enforce max sessions
	if t.config.maxSessions > 0 && t.sessionCount() >= t.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return
	}

	// Create session dispatcher and dispatch initialize
	dispatcher := t.server.newSession()
	resp := t.server.dispatchWith(dispatcher, r.Context(), claims, req)

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// If initialize failed (JSON-RPC error), return it without creating a session
	if resp.Error != nil {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(resp)
		w.Write(raw)
		return
	}

	// Success: create session and return with Mcp-Session-Id
	sessionID := generateSessionID()
	t.sessions.Store(sessionID, dispatcher)

	w.Header().Set(mcpSessionIDHeader, sessionID)
	w.Header().Set("Content-Type", "application/json")
	raw, _ := json.Marshal(resp)
	w.Write(raw)
}

// handleDelete handles DELETE requests: terminates a session.
func (t *streamableTransport) handleDelete(w http.ResponseWriter, r *http.Request) {
	// Auth check — prevent unauthenticated session termination
	if _, err := t.server.CheckAuth(r); err != nil {
		writeAuthError(w, err)
		return
	}

	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		http.Error(w, "missing "+mcpSessionIDHeader+" header", http.StatusBadRequest)
		return
	}

	if _, ok := t.sessions.LoadAndDelete(sessionID); !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// sessionCount returns the number of active sessions.
func (t *streamableTransport) sessionCount() int {
	count := 0
	t.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// shouldStreamSSE decides whether the server should return an SSE stream or a
// synchronous JSON response. The decision is deterministic, based on the client's
// Accept header and the JSON-RPC method.
//
// Per MCP spec (2025-11-25, Streamable HTTP transport):
//
//	"the server MUST either return Content-Type: text/event-stream, to initiate
//	 an SSE stream, or Content-Type: application/json, to return one JSON object."
//
// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
//
// Decision logic:
//
//	Accept has ONLY text/event-stream  → SSE for all requests (client's sole option)
//	Accept has ONLY application/json   → JSON always (no mid-request streaming possible)
//	Accept has BOTH                    → method-dependent:
//	  tools/call, prompts/get          → SSE (may emit progress/log notifications mid-execution)
//	  everything else                  → JSON (pure request-response, no streaming needed)
//	Notifications                      → never SSE (no response expected)
//
// Why not always JSON when both are accepted? Tool handlers call EmitProgress/EmitLog
// mid-execution. These notifications must reach the client *during* execution (e.g., for
// progress bars), not buffered until after the response. SSE is the only way to deliver
// them in real time over HTTP.
func shouldStreamSSE(accept string, req *Request) bool {
	if req.IsNotification() {
		return false
	}

	acceptsJSON, acceptsSSE := parseAcceptTypes(accept)

	if !acceptsSSE {
		return false
	}
	if !acceptsJSON {
		// Client only accepts SSE — use it for everything
		return true
	}
	// Client accepts both — SSE only for methods that may emit mid-request
	// notifications (progress, logging). All other methods use synchronous JSON.
	return req.Method == "tools/call" || req.Method == "prompts/get"
}

// parseAcceptTypes parses the Accept header into a set of accepted media types.
// Returns whether application/json and text/event-stream are present.
// Handles quality values (q=) and whitespace per RFC 7231 §5.3.2.
func parseAcceptTypes(accept string) (acceptsJSON, acceptsSSE bool) {
	for _, part := range strings.Split(accept, ",") {
		// Strip quality value and whitespace: "text/event-stream;q=0.9" → "text/event-stream"
		mediaType := strings.TrimSpace(part)
		if semi := strings.Index(mediaType, ";"); semi >= 0 {
			mediaType = strings.TrimSpace(mediaType[:semi])
		}
		switch mediaType {
		case "application/json":
			acceptsJSON = true
		case "text/event-stream":
			acceptsSSE = true
		case "*/*":
			acceptsJSON = true
			acceptsSSE = true
		}
	}
	return
}

// validateOrigin checks the Origin and Host headers to prevent DNS rebinding attacks.
// Per MCP spec: "Servers MUST validate the Origin header on all incoming connections.
// If the Origin header is present and invalid, servers MUST respond with HTTP 403."
//
// When allowedOrigins is configured, only those origins are accepted.
// When allowedOrigins is empty (default), only localhost variants are accepted.
func (t *streamableTransport) validateOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin header — check Host instead
		host := r.Host
		if host == "" {
			host = r.Header.Get("Host")
		}
		if host == "" {
			return true // No origin info to validate
		}
		return isLocalhostHost(host)
	}

	if len(t.config.allowedOrigins) > 0 {
		for _, allowed := range t.config.allowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	}

	// Default: accept only localhost origins
	return isLocalhostOrigin(origin)
}

// isLocalhostOrigin checks if an Origin header value is a localhost variant.
func isLocalhostOrigin(origin string) bool {
	for _, prefix := range []string{
		"http://localhost", "https://localhost",
		"http://127.0.0.1", "https://127.0.0.1",
		"http://[::1]", "https://[::1]",
	} {
		if origin == prefix || strings.HasPrefix(origin, prefix+":") {
			return true
		}
	}
	return false
}

// isLocalhostHost checks if a Host header value is a localhost variant.
func isLocalhostHost(host string) bool {
	// Strip port if present
	h := host
	if idx := strings.LastIndex(h, ":"); idx != -1 {
		h = h[:idx]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}

package mcpkit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	gohttp "github.com/panyam/servicekit/http"
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
	sseHub   *gohttp.SSEHub[SSEData] // for GET SSE streams (server-initiated notifications)
	config   transportConfig
}

// newStreamableTransport creates a Streamable HTTP transport.
func newStreamableTransport(s *Server, cfg transportConfig) *streamableTransport {
	return &streamableTransport{
		server: s,
		sseHub: gohttp.NewSSEHub[SSEData](),
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
		t.handleGet(w, r)
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

	// Stateless mode: every request gets a fresh dispatcher, no session tracking.
	// The dispatcher is auto-initialized so tool calls work without a separate
	// initialize handshake.
	if t.config.stateless {
		dispatcher := t.server.newSession()
		// Auto-initialize the dispatcher so it accepts any method
		dispatcher.initialized = true
		resp := t.server.dispatchWith(dispatcher, r.Context(), claims, &req)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(resp)
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

	// Validate MCP-Protocol-Version if present.
	// Per spec: "If the server receives a request with an invalid or unsupported
	// MCP-Protocol-Version, it MUST respond with 400 Bad Request."
	// We accept any supported version (not just the negotiated one) because
	// some clients may send a different supported version than was negotiated.
	if protoVer := r.Header.Get(mcpProtocolVersionHeader); protoVer != "" {
		supported := false
		for _, sv := range supportedProtocolVersions {
			if protoVer == sv {
				supported = true
				break
			}
		}
		if !supported {
			http.Error(w, "unsupported protocol version: "+protoVer, http.StatusBadRequest)
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

	// Build a request-scoped notifyFunc that writes to this SSE stream.
	// Passed through context (not mutating d.notifyFunc) to avoid races
	// when concurrent SSE-streaming POSTs share the same session dispatcher.
	requestNotify := NotifyFunc(func(method string, params any) {
		raw, err := marshalNotification(method, params)
		if err != nil {
			return
		}
		writeSSE(raw)
	})

	// Dispatch with the request-scoped notify — contextWithSession will use it
	// instead of d.notifyFunc.
	resp := t.server.dispatchWithNotify(d, r.Context(), claims, requestNotify, req)

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
	dispatcher.sessionID = sessionID
	t.sessions.Store(sessionID, dispatcher)

	w.Header().Set(mcpSessionIDHeader, sessionID)
	w.Header().Set("Content-Type", "application/json")
	raw, _ := json.Marshal(resp)
	w.Write(raw)
}

// handleGet handles GET requests: opens a long-lived SSE stream for
// server-initiated notifications (list-changed, resource updates, logging).
// Per MCP spec (2025-11-25, Streamable HTTP transport):
//
//	"Clients can open an HTTP GET request on the MCP endpoint to open an SSE stream.
//	 The server can use this stream to send notifications and requests to the client."
//
// The stream lives until the client disconnects or the session is deleted.
// Multiple GET streams can be open simultaneously for the same session.
// Uses servicekit's SSEServe/SSEHandler pattern for proper lifecycle management.
func (t *streamableTransport) handleGet(w http.ResponseWriter, r *http.Request) {
	handler := &streamableSSEHandler{transport: t}
	sseConfig := &gohttp.SSEConnConfig{
		KeepalivePeriod: t.config.keepalivePeriod,
	}
	gohttp.SSEServe[SSEData](handler, sseConfig)(w, r)
}

// streamableSSEHandler implements gohttp.SSEHandler for Streamable HTTP GET SSE.
// It validates auth + session, creates an SSE connection, and wires the session's
// notifyFunc to push to the SSE hub.
type streamableSSEHandler struct {
	transport *streamableTransport
}

// streamableSSEConn is the SSE connection for Streamable HTTP GET streams.
type streamableSSEConn struct {
	gohttp.BaseSSEConn[SSEData]
	sessionID  string
	transport  *streamableTransport
	dispatcher *Dispatcher
}

func (h *streamableSSEHandler) Validate(w http.ResponseWriter, r *http.Request) (*streamableSSEConn, bool) {
	// Auth check
	if _, err := h.transport.server.CheckAuth(r); err != nil {
		writeAuthError(w, err)
		return nil, false
	}

	// Session ID is optional on GET — if provided, attach to existing session;
	// if not, the stream is session-independent (receives broadcast notifications).
	sessionID := r.Header.Get(mcpSessionIDHeader)
	var dispatcher *Dispatcher
	if sessionID != "" {
		dispVal, ok := h.transport.sessions.Load(sessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return nil, false
		}
		dispatcher = dispVal.(*Dispatcher)
	} else {
		// No session — create a temporary dispatcher for this SSE stream
		sessionID = generateSessionID()
		dispatcher = h.transport.server.newSession()
		dispatcher.initialized = true
	}

	conn := &streamableSSEConn{
		BaseSSEConn: gohttp.BaseSSEConn[SSEData]{
			Codec:     &sseDataCodec{},
			ConnIdStr: sessionID,
			NameStr:   "MCP-GET-SSE",
		},
		sessionID:  sessionID,
		transport:  h.transport,
		dispatcher: dispatcher,
	}
	return conn, true
}

func (c *streamableSSEConn) OnStart(w http.ResponseWriter, r *http.Request) error {
	if err := c.BaseSSEConn.OnStart(w, r); err != nil {
		return err
	}

	// Register in hub for notification delivery
	c.transport.sseHub.Register(&c.BaseSSEConn)

	// Wire the dispatcher's notifyFunc to push to the SSE hub.
	// This enables EmitLog/Notify from tool handlers to reach the GET stream.
	sessionID := c.sessionID
	hub := c.transport.sseHub
	c.dispatcher.notifyFunc = func(method string, params any) {
		raw, err := marshalNotification(method, params)
		if err != nil {
			return
		}
		hub.SendEvent(sessionID, "message", SSEJSON(raw))
	}

	return nil
}

func (c *streamableSSEConn) OnClose() {
	c.transport.sseHub.Unregister(c.ConnId())
	c.BaseSSEConn.OnClose()
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

	d, ok := t.sessions.LoadAndDelete(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if disp := d.(*Dispatcher); disp.subManager != nil {
		disp.subManager.unsubscribeAll(sessionID)
	}

	w.WriteHeader(http.StatusOK)
}

// closeSession terminates a single session by ID. Returns true if found.
func (t *streamableTransport) closeSession(id string) bool {
	d, ok := t.sessions.LoadAndDelete(id)
	if ok {
		if disp := d.(*Dispatcher); disp.subManager != nil {
			disp.subManager.unsubscribeAll(id)
		}
	}
	return ok
}

// closeAllSessions terminates all active sessions.
func (t *streamableTransport) closeAllSessions() {
	t.sessions.Range(func(key, value any) bool {
		if disp := value.(*Dispatcher); disp.subManager != nil {
			disp.subManager.unsubscribeAll(key.(string))
		}
		t.sessions.Delete(key)
		return true
	})
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
//	Accept has BOTH                    → SSE (server prefers streaming for all request types)
//	Notifications                      → never SSE (no response expected)
//
// Per MCP spec: the server MUST return either text/event-stream or application/json.
// When the client accepts both, we prefer SSE because it enables mid-request
// notifications (progress, logging) and supports multiple concurrent streams
// (conformance requirement: server-sse-multiple-streams).
func shouldStreamSSE(accept string, req *Request) bool {
	if req.IsNotification() {
		return false
	}

	_, acceptsSSE := parseAcceptTypes(accept)
	if !acceptsSSE {
		return false
	}

	// Never stream SSE for initialize — the handshake must return JSON
	// so the client can parse the session ID and server capabilities.
	if req.Method == "initialize" {
		return false
	}

	return true
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

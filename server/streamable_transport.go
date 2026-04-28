package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	conc "github.com/panyam/gocurrent"
	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
	mw "github.com/panyam/servicekit/middleware"
)

const (
	// mcpSessionIDHeader is the HTTP header for session identity per MCP Streamable HTTP spec.
	mcpSessionIDHeader = "Mcp-Session-Id"

	// mcpProtocolVersionHeader is the HTTP header for protocol version per MCP spec.
	mcpProtocolVersionHeader = "MCP-Protocol-Version"
)

// streamableTransport implements the MCP Streamable HTTP transport (2025-03-26 spec).
// Each session is tracked via the Mcp-Session-Id header. Sessions are created on
// initialize and cleaned up via DELETE, idle timeout (WithSessionTimeout), or
// server restart.
//
// Unlike the SSE transport, there are no long-lived connections — each request is
// independent HTTP with the response returned directly in the body. Sessions are
// wrapped in sessionEntry which adds idle-timeout support with ref counting to
// prevent expiry during active requests.
type streamableTransport struct {
	server        *Server
	sessions      conc.SyncMap[string, *sessionEntry]
	sseHub        *gohttp.SSEHub[SSEData] // for GET SSE streams (server-initiated notifications)
	originChecker *mw.OriginChecker       // nil = allow all (set by allowedOrigins config)
	config        transportConfig
}

// sessionEntry wraps a Dispatcher with idle-timeout support. When a session
// timeout is configured, the timer fires after the session has been idle
// (no active POST requests or GET SSE streams) for the configured duration.
// Active requests are tracked via IdleTimer.Acquire/Release to prevent expiry mid-execution.
type sessionEntry struct {
	dispatcher *Dispatcher
	idleTimer  *conc.IdleTimer // nil-safe: all methods are no-ops on nil
	timeout    time.Duration   // retained for log messages on expiry

	// subject is the authenticated principal (Claims.Subject) bound at session
	// creation. Subsequent requests must match — prevents session hijacking
	// when different users share the same server. Empty when no auth is configured.
	subject string

	// getConn points at the live streamableSSEConn for this session's GET
	// SSE stream, if any. Set in OnStart, cleared in OnClose. Used by the
	// retry-hint path (#202) to emit raw SSE "retry:" fields to the
	// ongoing GET stream when a tool handler calls core.EmitSSERetry
	// during a POST. Nil when no GET stream is attached.
	//
	// Accessed atomically: the POST dispatch goroutine reads it (via the
	// sseRetry closure) while the GET stream's OnClose goroutine writes nil.
	getConn atomic.Pointer[streamableSSEConn]
}

// newStreamableTransport creates a Streamable HTTP transport.
func newStreamableTransport(s *Server, cfg transportConfig) *streamableTransport {
	// Build origin checker from config. Default (no allowedOrigins) = localhost-only.
	var checker *mw.OriginChecker
	if len(cfg.allowedOrigins) > 0 {
		checker = mw.NewOriginChecker(cfg.allowedOrigins)
	} else {
		checker = mw.NewLocalhostOriginChecker()
	}

	return &streamableTransport{
		server:        s,
		sseHub:        gohttp.NewSSEHub[SSEData](),
		originChecker: checker,
		config:        cfg,
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
	if !t.originChecker.CheckRequest(r) {
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

// expireSession removes an idle session and cleans up its resources.
// Called by the session timer when the idle timeout fires.
func (t *streamableTransport) expireSession(id string) {
	entry, ok := t.sessions.LoadAndDelete(id)
	if !ok {
		return
	}
	entry.dispatcher.Close()
	// Close any GET SSE streams for this session
	t.sseHub.Unregister(id)
	if t.config.eventStore != nil {
		t.config.eventStore.Trim(id)
	}
	log.Printf("mcpkit: session %s expired after %s idle", id, entry.timeout)
	t.server.notifySessionExpire(id, fmt.Errorf("idle timeout (%s)", entry.timeout))
}

// loadSession loads a sessionEntry by ID. Returns (entry, true) or (nil, false).
func (t *streamableTransport) loadSession(id string) (*sessionEntry, bool) {
	return t.sessions.Load(id)
}

// verifySessionPrincipal checks that the request's authenticated principal
// matches the session's bound principal. Returns true if allowed, false if
// rejected (403 already written). Sessions created without auth (empty subject)
// allow any caller — backward compatible with unauthenticated servers.
func (e *sessionEntry) verifyPrincipal(w http.ResponseWriter, claims *core.Claims) bool {
	if e.subject == "" {
		return true // no auth binding
	}
	if claims != nil && claims.Subject == e.subject {
		return true // principal matches
	}
	http.Error(w, "forbidden: session principal mismatch", http.StatusForbidden)
	return false
}

// handlePost handles POST requests: JSON-RPC dispatch with session management.
func (t *streamableTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	// NOTE: Per MCP spec (2025-11-25, Streamable HTTP transport), clients MUST include
	// Accept header that accepts both application/json and text/event-stream.
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	// We validate this client-side (core.StreamableHTTPAccept constant) but do NOT reject
	// non-conforming requests server-side — the spec places the MUST on the client,
	// and rejecting would break backward compatibility with older clients.

	// Content-Type validation: reject non-JSON POST requests (CSRF defense-in-depth).
	// Per MCP spec: "HTTP clients sending requests to the server MUST set the
	// Content-Type header to application/json."
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Read body first — needed for method peek and dispatch.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Auth check — peek at method for public method bypass.
	var claims *core.Claims
	if method := extractMethodFromJSON(body); t.server.IsPublicMethod(method) {
		// Public method — skip auth, dispatch without claims.
		claims, _ = t.server.CheckAuth(r) // best-effort: populate claims if token present
	} else {
		claims, err = t.server.CheckAuth(r)
		if err != nil {
			writeAuthError(w, err)
			return
		}
	}

	// Detect if the incoming message is a JSON-RPC response (from the client
	// answering a server-to-client request like sampling/createMessage).
	// Must check before parsing as core.Request since responses have no "method" field.
	if core.IsJSONRPCResponse(body) {
		sessionID := r.Header.Get(mcpSessionIDHeader)
		if sessionID == "" {
			http.Error(w, "missing "+mcpSessionIDHeader+" header", http.StatusBadRequest)
			return
		}
		entry, ok := t.loadSession(sessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if !entry.verifyPrincipal(w, claims) {
			return
		}
		entry.idleTimer.Acquire()
		defer entry.idleTimer.Release()
		var resp core.Response
		if err := json.Unmarshal(body, &resp); err == nil {
			entry.dispatcher.RouteResponse(&resp)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// JSON-RPC 2.0 batch request: array of request objects.
	// Per spec Section 6: dispatch each, collect responses, return as JSON array.
	if gohttp.DetectBatch(body) {
		t.handleBatchPost(w, r, claims, body)
		return
	}

	var req core.Request
	if err := json.Unmarshal(body, &req); err != nil {
		// Parse error → JSON-RPC error in response body
		w.Header().Set("Content-Type", "application/json")
		errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "parse error: "+err.Error())
		raw, _ := marshalJSON(errResp)
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
		resp, dErr := t.server.dispatchWith(dispatcher, r.Context(), claims, &req)
		if dErr != nil {
			writeAuthError(w, dErr)
			return
		}
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := marshalJSON(resp)
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

	entry, ok := t.loadSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if !entry.verifyPrincipal(w, claims) {
		return
	}
	entry.idleTimer.Acquire()
	defer entry.idleTimer.Release()
	dispatcher := entry.dispatcher

	// Validate MCP-Protocol-Version if present.
	// Per spec: "If the server receives a request with an invalid or unsupported
	// MCP-Protocol-Version, it MUST respond with 400 Bad core.Request."
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
	resp, dErr := t.server.dispatchWith(dispatcher, r.Context(), claims, &req)
	if dErr != nil {
		writeAuthError(w, dErr)
		return
	}

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	raw, err := marshalJSON(resp)
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
func (t *streamableTransport) handlePostSSE(w http.ResponseWriter, r *http.Request, claims *core.Claims, d *Dispatcher, req *core.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fall back to synchronous JSON if flushing not supported
		resp, dErr := t.server.dispatchWith(d, r.Context(), claims, req)
		if dErr != nil {
			writeAuthError(w, dErr)
			return
		}
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := marshalJSON(resp)
		w.Write(raw)
		return
	}

	// SSE headers are set lazily on first write so we can still surface
	// transport-level auth errors via writeAuthError (HTTP 403/401 +
	// WWW-Authenticate) when middleware short-circuits before any
	// notification has been emitted.
	var mu sync.Mutex
	var closed bool   // set after handler returns; silently drops writes from background goroutines
	var sseStarted bool // set on first writeSSE call; once true, headers are committed
	writeSSE := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return // POST response already sent — background goroutine writing to dead writer
		}
		if !sseStarted {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			sseStarted = true
		}
		emitSSEEvent(d.eventIDs, t.config.eventStore, d.sessionID, data, func(id string, data json.RawMessage) {
			fmt.Fprintf(w, "id: %s\nevent: message\ndata: %s\n\n", id, data)
			flusher.Flush()
		})
	}

	// Build request-scoped notifyFunc and requestFunc that write to this SSE stream.
	// Passed through context (not mutating d.notifyFunc) to avoid races
	// when concurrent SSE-streaming POSTs share the same session dispatcher.
	requestNotify := core.NotifyFunc(func(method string, params any) {
		raw, err := core.MarshalNotification(method, params)
		if err != nil {
			return
		}
		writeSSE(raw)
	})

	// core.RequestFunc scoped to this SSE stream — server-to-client requests
	// (sampling/createMessage, elicitation/create) are pushed as SSE events
	// on this open response stream. The client must POST the response back.
	requestFunc := d.makeRequestFunc(func(raw json.RawMessage) {
		writeSSE(raw)
	})

	// Retry-hint emitter: routes EmitSSERetry from the POST handler to the
	// session's long-lived GET SSE stream (if one is open). Looks up the
	// conn on each call so a reconnect during a long-running tool picks up
	// the new stream automatically (#202).
	sseRetry := func(ms int) {
		if entry, ok := t.loadSession(d.sessionID); ok {
			if conn := entry.getConn.Load(); conn != nil {
				conn.SendRetry(ms)
			}
		}
	}

	// Dispatch with request-scoped notify, request, and retry funcs.
	resp, dErr := t.server.dispatchWithOpts(d, r.Context(), claims, requestNotify, requestFunc, sseRetry, req)

	// Transport-level error from middleware (e.g., scope step-up): if the SSE
	// stream hasn't started yet, we can still send an HTTP-level auth response
	// (writeAuthError → 401/403 + WWW-Authenticate). If the stream has already
	// flushed (a notification fired before the middleware error — unusual but
	// possible), we fall back to emitting a JSON-RPC error on the SSE stream.
	if dErr != nil {
		mu.Lock()
		started := sseStarted
		mu.Unlock()
		if !started {
			writeAuthError(w, dErr)
		} else {
			errResp := core.NewErrorResponse(req.ID, core.ErrCodeServerError, dErr.Error())
			raw, _ := marshalJSON(errResp)
			writeSSE(raw)
		}
	} else if resp != nil {
		// Write the JSON-RPC response as the final SSE event
		raw, _ := marshalJSON(resp)
		writeSSE(raw)
	}

	// Mark the POST-scoped writer as closed. Background goroutines that
	// inherited this notifyFunc/requestFunc via the context will get
	// silent no-ops instead of panicking on the dead ResponseWriter.
	mu.Lock()
	closed = true
	mu.Unlock()
}

// handleInitialize handles POST initialize: creates session, dispatches, returns
// the response with Mcp-Session-Id header.
func (t *streamableTransport) handleInitialize(w http.ResponseWriter, r *http.Request, claims *core.Claims, req *core.Request) {
	// Enforce max sessions
	if t.config.maxSessions > 0 && t.sessionCount() >= t.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return
	}

	// Create session dispatcher and dispatch initialize
	dispatcher := t.server.newSession()
	resp, dErr := t.server.dispatchWith(dispatcher, r.Context(), claims, req)
	if dErr != nil {
		writeAuthError(w, dErr)
		return
	}

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// If initialize failed (JSON-RPC error), return it without creating a session
	if resp.Error != nil {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := marshalJSON(resp)
		w.Write(raw)
		return
	}

	// Success: create session and return with Mcp-Session-Id.
	// Check if client suggested a session ID via _suggestedSessionId.
	sessionID := t.resolveSessionID(req.Params)
	dispatcher.sessionID = sessionID
	// Bind authenticated principal to session — prevents hijacking.
	subject := ""
	if claims != nil && claims.Subject != "" {
		subject = claims.Subject
	}
	entry := &sessionEntry{
		dispatcher: dispatcher,
		idleTimer:  conc.NewIdleTimer(t.config.sessionTimeout, func() { t.expireSession(sessionID) }),
		timeout:    t.config.sessionTimeout,
		subject:    subject,
	}
	t.sessions.Store(sessionID, entry)

	w.Header().Set(mcpSessionIDHeader, sessionID)

	// Return SSE when the client accepts it (matching TS SDK behavior).
	// Fall back to JSON when the client only accepts JSON.
	_, acceptsSSE := gohttp.ParseAcceptTypes(r.Header.Get("Accept"))
	if acceptsSSE {
		if flusher, ok := w.(http.Flusher); ok {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			raw, _ := marshalJSON(resp)
			emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
				fmt.Fprintf(w, "id: %s\nevent: message\ndata: %s\n\n", id, data)
				flusher.Flush()
			})
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	raw, _ := marshalJSON(resp)
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
	entry      *sessionEntry        // non-nil if attached to an existing session (for release on close)
	keepalive  *sessionKeepalive    // non-nil if keepalive is configured
}

func (h *streamableSSEHandler) Validate(w http.ResponseWriter, r *http.Request) (*streamableSSEConn, bool) {
	// Auth check
	claims, err := h.transport.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return nil, false
	}

	// Session ID is optional on GET — if provided, attach to existing session;
	// if not, the stream is session-independent (receives broadcast notifications).
	sessionID := r.Header.Get(mcpSessionIDHeader)
	var dispatcher *Dispatcher
	if sessionID != "" {
		entry, ok := h.transport.loadSession(sessionID)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return nil, false
		}
		if !entry.verifyPrincipal(w, claims) {
			return nil, false
		}
		entry.idleTimer.Acquire() // released in OnClose
		dispatcher = entry.dispatcher
	} else {
		// No session — create a temporary dispatcher for this SSE stream
		sessionID = gohttp.GenerateSessionID()
		dispatcher = h.transport.server.newSession()
		dispatcher.initialized = true
	}

	// Look up the entry for release tracking (nil for session-independent streams)
	var connEntry *sessionEntry
	if r.Header.Get(mcpSessionIDHeader) != "" {
		connEntry, _ = h.transport.loadSession(sessionID)
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
		entry:      connEntry,
	}
	return conn, true
}

func (c *streamableSSEConn) OnStart(w http.ResponseWriter, r *http.Request) error {
	if err := c.BaseSSEConn.OnStart(w, r); err != nil {
		return err
	}

	// Register in hub for notification delivery
	c.transport.sseHub.Register(&c.BaseSSEConn)

	// Track the live GET conn on the session entry so the POST dispatch
	// path can route EmitSSERetry hints to it (#202).
	if c.entry != nil {
		c.entry.getConn.Store(c)
	}

	// Wire notifyFunc, pushRequest, and keepalive via the shared SSE
	// transport wiring helper (#199).
	sessionID := c.sessionID
	wiring := &sseWiring{
		dispatcher: c.dispatcher,
		hub:        c.transport.sseHub,
		sessionID:  sessionID,
		store:      c.transport.config.eventStore,
		cfg:        c.transport.config,
		onDeath:    func() { c.transport.expireSession(sessionID) },
		onPingFail: func(failures int) { c.transport.server.notifyKeepaliveFailure(sessionID, failures) },
	}
	_, c.keepalive = wiring.wire()

	// Replay missed events if client reconnected with Last-Event-ID.
	// Must happen AFTER wiring so pushFunc is set (replay uses the hub).
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" && c.transport.config.eventStore != nil {
		events, _ := c.transport.config.eventStore.Replay(sessionID, lastID)
		for _, ev := range events {
			c.SendEventWithID(ev.Event, ev.ID, SSEJSON(ev.Data))
		}
	}

	if c.keepalive != nil && c.entry != nil {
		c.keepalive.start()
	}

	return nil
}

func (c *streamableSSEConn) OnClose() {
	if c.keepalive != nil {
		c.keepalive.stop()
	}
	// Clear the persistent push function: the underlying hub connection is
	// about to go away. A later GET SSE stream will re-wire via OnStart.
	c.dispatcher.SetPushRequest(nil)
	c.transport.sseHub.Unregister(c.ConnId())
	if c.entry != nil {
		// Clear the GET conn pointer only if it still points at us —
		// a rapid close+reconnect could have replaced it already (#202).
		c.entry.getConn.CompareAndSwap(c, nil)
		c.entry.idleTimer.Release()
	}
	c.BaseSSEConn.OnClose()
}

// handleDelete handles DELETE requests: terminates a session.
func (t *streamableTransport) handleDelete(w http.ResponseWriter, r *http.Request) {
	// Auth check — prevent unauthenticated session termination
	claims, err := t.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		http.Error(w, "missing "+mcpSessionIDHeader+" header", http.StatusBadRequest)
		return
	}

	// Look up session first (without deleting) to verify principal.
	entry, ok := t.loadSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if !entry.verifyPrincipal(w, claims) {
		return
	}
	// Principal verified — now delete.
	t.sessions.Delete(sessionID)
	entry.idleTimer.Stop()
	entry.dispatcher.Close()
	if t.config.eventStore != nil {
		t.config.eventStore.Trim(sessionID)
	}

	w.WriteHeader(http.StatusOK)
}

// closeSession terminates a single session by ID. Returns true if found.
func (t *streamableTransport) closeSession(id string) bool {
	entry, ok := t.sessions.LoadAndDelete(id)
	if ok {
		entry.idleTimer.Stop()
		entry.dispatcher.Close()
		if t.config.eventStore != nil {
			t.config.eventStore.Trim(id)
		}
	}
	return ok
}

// closeAllSessions terminates all active sessions.
func (t *streamableTransport) closeAllSessions() {
	t.sessions.Range(func(key string, entry *sessionEntry) bool {
		entry.idleTimer.Stop()
		entry.dispatcher.Close()
		t.sessions.Delete(key)
		return true
	})
}

// handleBatchPost handles a JSON-RPC 2.0 batch request (JSON array of
// request objects). Each request is dispatched sequentially, responses are
// collected, and the combined response array is returned as JSON.
// Notifications (requests with no ID) produce no response entry.
// Per JSON-RPC 2.0 spec Section 6.
func (t *streamableTransport) handleBatchPost(w http.ResponseWriter, r *http.Request, claims *core.Claims, body []byte) {
	parts, err := gohttp.SplitBatch(body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "invalid batch: "+err.Error())
		raw, _ := marshalJSON(errResp)
		w.Write(raw)
		return
	}

	if len(parts) == 0 {
		w.Header().Set("Content-Type", "application/json")
		errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeInvalidRequest, "empty batch")
		raw, _ := marshalJSON(errResp)
		w.Write(raw)
		return
	}

	// Require session for batch (batch cannot contain initialize as first request)
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		http.Error(w, "missing "+mcpSessionIDHeader+" header for batch request", http.StatusBadRequest)
		return
	}
	entry, ok := t.loadSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if !entry.verifyPrincipal(w, claims) {
		return
	}
	entry.idleTimer.Acquire()
	defer entry.idleTimer.Release()

	var responses []json.RawMessage
	for _, part := range parts {
		var req core.Request
		if err := json.Unmarshal(part, &req); err != nil {
			errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "parse error in batch element: "+err.Error())
			raw, _ := marshalJSON(errResp)
			responses = append(responses, raw)
			continue
		}

		resp, dErr := t.server.dispatchWith(entry.dispatcher, r.Context(), claims, &req)
		if dErr != nil {
			// Surface transport-level error as a JSON-RPC error in this batch slot.
			errResp := core.NewErrorResponse(req.ID, core.ErrCodeServerError, dErr.Error())
			raw, _ := marshalJSON(errResp)
			responses = append(responses, raw)
			continue
		}
		if resp == nil {
			continue // notification — no response entry
		}
		raw, _ := marshalJSON(resp)
		responses = append(responses, raw)
	}

	if len(responses) == 0 {
		// All were notifications — no response body
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// Build JSON array manually to avoid double-encoding
	w.Write([]byte("["))
	for i, r := range responses {
		if i > 0 {
			w.Write([]byte(","))
		}
		w.Write(r)
	}
	w.Write([]byte("]"))
}

// broadcast sends a notification to all active Streamable HTTP sessions.
// Sessions without a GET SSE stream have nil notifyFunc and are skipped safely.
func (t *streamableTransport) broadcast(method string, params any) {
	t.sessions.Range(func(_ string, entry *sessionEntry) bool {
		d := entry.dispatcher
		if fn := d.getNotifyFunc(); fn != nil {
			fn(method, params)
		}
		return true
	})
}

// resolveSessionID determines the session ID to use for a new session.
// If the client suggested a valid, unique ID via _suggestedSessionId in the
// initialize params, it's used. Otherwise a random ID is generated.
func (t *streamableTransport) resolveSessionID(params json.RawMessage) string {
	var p struct {
		SuggestedSessionID string `json:"_suggestedSessionId"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if id := p.SuggestedSessionID; id != "" && validateSessionID(id) {
		// Check uniqueness — reject if already in use
		if _, exists := t.sessions.Load(id); !exists {
			return id
		}
	}
	return gohttp.GenerateSessionID()
}

// validateSessionID checks if a client-suggested session ID is valid:
// non-empty, <= 128 chars, alphanumeric + hyphens + underscores + dots.
func validateSessionID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// sessionCount returns the number of active sessions.
func (t *streamableTransport) sessionCount() int {
	count := 0
	t.sessions.Range(func(_ string, _ *sessionEntry) bool {
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
func shouldStreamSSE(accept string, req *core.Request) bool {
	if req.IsNotification() {
		return false
	}

	_, acceptsSSE := gohttp.ParseAcceptTypes(accept)
	if !acceptsSSE {
		return false
	}

	return true
}

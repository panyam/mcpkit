package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	conc "github.com/panyam/gocurrent"
	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// sseSessionEntry wraps a Dispatcher with session metadata and an optional
// grace period timer. When a grace period is configured, the session survives
// brief disconnects — the Dispatcher stays alive while the timer counts down,
// allowing reconnection without re-initialization.
type sseSessionEntry struct {
	dispatcher  *Dispatcher
	graceTimer  *conc.IdleTimer // nil when grace period not configured; nil-safe
	subject     string          // auth principal binding (empty if no auth)
	connID      string          // current SSE hub connection ID (empty during grace period)
	gracePeriod time.Duration   // retained for log messages

	// conn points at the live mcpSSEConn for this session. Used by the retry
	// hint path (#72) to emit raw SSE "retry:" fields to the current stream
	// without going through the hub's SendEventWithID abstraction. Reset on
	// reconnect. Nil during grace period (between OnClose and OnStart).
	conn *mcpSSEConn
}

// sseTransport implements the MCP HTTP+SSE transport (2024-11-05 spec).
// Each SSE connection is an independent MCP session with its own Dispatcher.
// With WithSSEGracePeriod, sessions can survive brief disconnects.
type sseTransport struct {
	server   *Server
	hub      *gohttp.SSEHub[SSEData]
	sessions conc.SyncMap[string, *sseSessionEntry]
	config   transportConfig
}

// newSSETransport creates an SSE transport for the given server.
func newSSETransport(s *Server, opts ...TransportOption) *sseTransport {
	cfg := defaultTransportConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &sseTransport{
		server: s,
		hub:    gohttp.NewSSEHub[SSEData](),
		config: cfg,
	}
}

// handler returns an http.Handler that serves the SSE and message endpoints.
// Used when SSE is the only transport. Includes base prefix routing.
func (t *sseTransport) handler() http.Handler {
	mux := http.NewServeMux()
	prefix := strings.TrimRight(t.config.prefix, "/")

	t.mountOn(mux, prefix)

	// Base prefix handler: many MCP clients (including the MCP Inspector)
	// connect to the base URL rather than appending /sse. Route by method:
	//   GET  → SSE stream (same as /sse)
	//   POST → JSON-RPC message (same as /message)
	sseServeFunc := t.sseServeFunc()
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			sseServeFunc(w, r)
		case http.MethodPost:
			t.handleMessage(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return mux
}

// mountOn registers the SSE transport's canonical endpoints on an external mux.
// Used when composing SSE with Streamable HTTP (which owns the base prefix).
func (t *sseTransport) mountOn(mux *http.ServeMux, prefix string) {
	sseServeFunc := t.sseServeFunc()
	mux.HandleFunc(prefix+"/sse", sseServeFunc)
	mux.HandleFunc(prefix+"/message", t.handleMessage)
}

// sseServeFunc returns the HTTP handler for SSE connections.
func (t *sseTransport) sseServeFunc() http.HandlerFunc {
	sseHandler := &mcpSSEHandler{transport: t}
	sseConfig := &gohttp.SSEConnConfig{
		KeepalivePeriod: t.config.keepalivePeriod,
	}
	return gohttp.SSEServe[SSEData](sseHandler, sseConfig)
}

// postURL builds the POST URL for the endpoint event.
func (t *sseTransport) postURL(r *http.Request) string {
	prefix := strings.TrimRight(t.config.prefix, "/")
	if t.config.publicURL != "" {
		return strings.TrimRight(t.config.publicURL, "/") + prefix + "/message"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + prefix + "/message"
}

// handleMessage handles POST /message?sessionId=<id> requests.
// It reads a JSON-RPC request from the body, dispatches it through the
// per-session Dispatcher, and pushes the response on the SSE stream.
func (t *sseTransport) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Content-Type validation: reject non-JSON POST requests (CSRF defense-in-depth).
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Auth check
	claims, err := t.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId", http.StatusBadRequest)
		return
	}

	entry, ok := t.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "session not found or expired", http.StatusGone)
		return
	}
	dispatcher := entry.dispatcher

	// Verify the POST principal matches the session-opening principal.
	// Prevents user B (with a valid token) from posting to user A's session.
	if entry.subject != "" {
		if claims == nil || claims.Subject != entry.subject {
			http.Error(w, "forbidden: session principal mismatch", http.StatusForbidden)
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Detect if the incoming message is a JSON-RPC response (from the client
	// answering a server-to-client request like sampling/createMessage).
	// Responses have an "id" field but no "method" field.
	if core.IsJSONRPCResponse(body) {
		var resp core.Response
		if err := json.Unmarshal(body, &resp); err == nil {
			dispatcher.RouteResponse(&resp)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// JSON-RPC 2.0 batch request: dispatch each, push responses as SSE events.
	if gohttp.DetectBatch(body) {
		parts, splitErr := gohttp.SplitBatch(body)
		if splitErr != nil {
			errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "invalid batch: "+splitErr.Error())
			raw, _ := json.Marshal(errResp)
			emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
				t.hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
			})
		} else {
			for _, part := range parts {
				var batchReq core.Request
				if err := json.Unmarshal(part, &batchReq); err != nil {
					errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "parse error in batch element: "+err.Error())
					raw, _ := json.Marshal(errResp)
					emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
						t.hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
					})
					continue
				}
				resp := t.server.dispatchWith(dispatcher, r.Context(), claims, &batchReq)
				if resp == nil {
					continue // notification — no response
				}
				raw, _ := json.Marshal(resp)
				emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
					t.hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
				})
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var req core.Request
	if err := json.Unmarshal(body, &req); err != nil {
		// JSON parse error — push error response on SSE stream
		errResp := core.NewErrorResponse(json.RawMessage("null"), core.ErrCodeParse, "parse error: "+err.Error())
		raw, _ := json.Marshal(errResp)
		emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
			t.hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
		})
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Build request func scoped to this session's SSE stream.
	hubSend := func(id string, data json.RawMessage) {
		t.hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
	}
	requestFunc := dispatcher.makeRequestFunc(func(raw json.RawMessage) {
		emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, hubSend)
	})
	// Retry-hint emitter: looks up the live conn each time so a reconnect
	// during a long-running tool picks up the new conn automatically (#72).
	sseRetry := func(ms int) {
		if cur, ok := t.sessions.Load(sessionID); ok && cur.conn != nil {
			cur.conn.SendRetry(ms)
		}
	}
	resp := t.server.dispatchWithOpts(dispatcher, r.Context(), claims, dispatcher.getNotifyFunc(), requestFunc, sseRetry, &req)

	// Notifications have no response
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	emitSSEEvent(dispatcher.eventIDs, t.config.eventStore, sessionID, raw, hubSend)
	w.WriteHeader(http.StatusAccepted)
}

// mcpSSEConn is the per-session SSE connection for MCP.
// It embeds BaseSSEConn[any] and adds session tracking.
// When reconnecting is true, the connection reuses an existing session entry
// instead of creating a new Dispatcher.
type mcpSSEConn struct {
	gohttp.BaseSSEConn[SSEData]
	sessionID    string
	transport    *sseTransport
	request      *http.Request      // stored for building the POST URL
	keepalive    *sessionKeepalive  // non-nil if keepalive is configured
	reconnecting bool               // true when resuming an existing session
	entry        *sseSessionEntry   // non-nil when reconnecting
}

func (c *mcpSSEConn) OnStart(w http.ResponseWriter, r *http.Request) error {
	c.request = r
	if err := c.BaseSSEConn.OnStart(w, r); err != nil {
		return err
	}

	// Register new hub connection.
	c.transport.hub.Register(&c.BaseSSEConn)

	sessionID := c.sessionID
	hub := c.transport.hub
	store := c.transport.config.eventStore
	cfg := c.transport.config

	var dispatcher *Dispatcher
	var entry *sseSessionEntry

	if c.reconnecting && c.entry != nil {
		// Reconnecting to an existing session within grace period.
		entry = c.entry
		dispatcher = entry.dispatcher
		entry.connID = c.ConnId()
		entry.conn = c
	} else {
		// New session — create Dispatcher and session entry.
		dispatcher = c.transport.server.newSession()
		dispatcher.sessionID = sessionID

		// Extract subject from the temporary entry set in Validate().
		subject := ""
		if c.entry != nil {
			subject = c.entry.subject
		}

		var graceTimer *conc.IdleTimer
		if cfg.sseGracePeriod > 0 {
			graceTimer = conc.NewIdleTimer(cfg.sseGracePeriod, func() {
				c.transport.expireSSESession(sessionID)
			})
			graceTimer.Acquire() // connection is active
		}

		entry = &sseSessionEntry{
			dispatcher:  dispatcher,
			graceTimer:  graceTimer,
			subject:     subject,
			connID:      c.ConnId(),
			gracePeriod: cfg.sseGracePeriod,
			conn:        c,
		}
		c.transport.sessions.Store(sessionID, entry)
	}

	// Wire up (or re-wire) server-to-client notifications via the SSE stream.
	// The closure captures the current hub connection, so it must be refreshed
	// on reconnect to point to the new connection.
	dispatcher.SetNotifyFunc(func(method string, params any) {
		raw, err := core.MarshalNotification(method, params)
		if err != nil {
			return
		}
		emitSSEEvent(dispatcher.eventIDs, store, sessionID, raw, func(id string, data json.RawMessage) {
			hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
		})
	})

	// Persistent push function for server-initiated JSON-RPC requests
	// (roots/list, keepalive ping, any future out-of-band request). Re-wired
	// on reconnect so the closure points to the current hub connection.
	pushFunc := func(raw json.RawMessage) {
		emitSSEEvent(dispatcher.eventIDs, cfg.eventStore, sessionID, raw, func(id string, data json.RawMessage) {
			hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
		})
	}
	dispatcher.SetPushRequest(pushFunc)

	// Start keepalive pings if configured.
	if cfg.keepaliveInterval > 0 {
		maxFails := cfg.keepaliveMaxFails
		if maxFails <= 0 {
			maxFails = 3
		}
		c.keepalive = &sessionKeepalive{
			interval:    cfg.keepaliveInterval,
			maxFailures: maxFails,
			requestFunc: dispatcher.makeRequestFunc(pushFunc),
			onDeath:     func() { c.transport.closeSession(sessionID) },
			onPingFail:  func(failures int) { c.transport.server.notifyKeepaliveFailure(sessionID, failures) },
		}
		c.keepalive.start()
	}

	// Send endpoint event with the POST URL as raw text.
	postURL := c.transport.postURL(r) + "?sessionId=" + c.sessionID
	c.SendEvent("endpoint", SSEText(postURL))

	// Replay missed events after the endpoint event so the client knows
	// where to POST before receiving replayed responses.
	if c.reconnecting {
		if lastID := r.Header.Get("Last-Event-ID"); lastID != "" && store != nil {
			events, _ := store.Replay(sessionID, lastID)
			for _, ev := range events {
				c.SendEventWithID(ev.Event, ev.ID, SSEJSON(ev.Data))
			}
		}
	}

	return nil
}

func (c *mcpSSEConn) OnClose() {
	if c.keepalive != nil {
		c.keepalive.stop()
	}

	// Clear the persistent push function: the hub connection is going away.
	// If the session survives a grace-period reconnect, OnStart re-wires it.
	entry, ok := c.transport.sessions.Load(c.sessionID)
	if ok && entry.dispatcher != nil {
		entry.dispatcher.SetPushRequest(nil)
	}

	if !ok {
		c.transport.hub.Unregister(c.ConnId())
		c.BaseSSEConn.OnClose()
		return
	}

	// Unregister the physical SSE connection from the hub.
	c.transport.hub.Unregister(c.ConnId())
	entry.connID = ""
	entry.conn = nil

	if entry.graceTimer != nil {
		// Grace period configured — keep the session alive. The timer starts
		// counting down when Release is called (active count drops to 0).
		// If no reconnection arrives, expireSSESession cleans up.
		entry.graceTimer.Release()
	} else {
		// No grace period — immediate cleanup (backward compatible).
		entry.dispatcher.Close()
		c.transport.sessions.Delete(c.sessionID)
		if store := c.transport.config.eventStore; store != nil {
			store.Trim(c.sessionID)
		}
	}

	c.BaseSSEConn.OnClose()
}

// expireSSESession cleans up a session after its grace period expires
// without reconnection. Called by the IdleTimer callback.
func (t *sseTransport) expireSSESession(id string) {
	entry, ok := t.sessions.LoadAndDelete(id)
	if !ok {
		return
	}
	entry.dispatcher.Close()
	if t.config.eventStore != nil {
		t.config.eventStore.Trim(id)
	}
	log.Printf("mcpkit: SSE session %s expired after %s grace period", id, entry.gracePeriod)
	t.server.notifySessionExpire(id, fmt.Errorf("grace period expired (%s)", entry.gracePeriod))
}

// closeSession terminates a single SSE session by ID.
// Unregisters from the hub (closing the SSE stream) and removes from session maps.
func (t *sseTransport) closeSession(id string) bool {
	entry, ok := t.sessions.LoadAndDelete(id)
	if !ok {
		return false
	}
	entry.dispatcher.Close()
	if entry.graceTimer != nil {
		entry.graceTimer.Stop()
	}
	if entry.connID != "" {
		t.hub.Unregister(entry.connID)
	}
	return true
}

// closeAllSessions terminates all active SSE sessions.
func (t *sseTransport) closeAllSessions() {
	t.sessions.Range(func(key string, entry *sseSessionEntry) bool {
		entry.dispatcher.Close()
		if entry.graceTimer != nil {
			entry.graceTimer.Stop()
		}
		if entry.connID != "" {
			t.hub.Unregister(entry.connID)
		}
		t.sessions.Delete(key)
		return true
	})
}

// broadcast sends a notification to all active SSE sessions.
// Sessions with nil notifyFunc are skipped safely.
func (t *sseTransport) broadcast(method string, params any) {
	t.sessions.Range(func(_ string, entry *sseSessionEntry) bool {
		if fn := entry.dispatcher.getNotifyFunc(); fn != nil {
			fn(method, params)
		}
		return true
	})
}

// mcpSSEHandler implements gohttp.SSEHandler for MCP session creation.
type mcpSSEHandler struct {
	transport *sseTransport
}

func (h *mcpSSEHandler) Validate(w http.ResponseWriter, r *http.Request) (*mcpSSEConn, bool) {
	// Auth check — required for both new connections and reconnections.
	claims, err := h.transport.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return nil, false
	}

	// Check for reconnection: client provides sessionId query param from
	// a previous connection. If the session is still alive (within grace
	// period), reuse it instead of creating a new one.
	if reqSessionID := r.URL.Query().Get("sessionId"); reqSessionID != "" {
		if entry, ok := h.transport.sessions.Load(reqSessionID); ok {
			// Verify principal matches — prevents session hijacking.
			if entry.subject != "" {
				if claims == nil || claims.Subject != entry.subject {
					http.Error(w, "forbidden: session principal mismatch", http.StatusForbidden)
					return nil, false
				}
			}
			// Cancel the grace timer — session is being resumed.
			if entry.graceTimer != nil {
				entry.graceTimer.Acquire()
			}
			conn := &mcpSSEConn{
				BaseSSEConn: gohttp.BaseSSEConn[SSEData]{
					Codec:     &sseDataCodec{},
					ConnIdStr: reqSessionID, // reuse session ID as hub connection ID
					NameStr:   "MCP",
				},
				sessionID:    reqSessionID,
				transport:    h.transport,
				reconnecting: true,
				entry:        entry,
			}
			return conn, true
		}
		// Session expired — return 410 Gone.
		http.Error(w, "session not found or expired", http.StatusGone)
		return nil, false
	}

	// New session — enforce max sessions limit.
	if h.transport.config.maxSessions > 0 && h.transport.hub.Count() >= h.transport.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return nil, false
	}

	sessionID := gohttp.GenerateSessionID()

	// Determine the authenticated subject for principal binding.
	subject := ""
	if claims != nil && claims.Subject != "" {
		subject = claims.Subject
	}

	conn := &mcpSSEConn{
		BaseSSEConn: gohttp.BaseSSEConn[SSEData]{
			Codec:     &sseDataCodec{},
			ConnIdStr: sessionID,
			NameStr:   "MCP",
		},
		sessionID: sessionID,
		transport: h.transport,
	}
	// Store subject temporarily on the conn so OnStart can use it when
	// creating the sseSessionEntry. For new sessions, entry is nil.
	conn.entry = &sseSessionEntry{subject: subject}
	return conn, true
}

// SSEData represents SSE event data that is either raw text or pre-encoded JSON.
// MCP uses both: the "endpoint" event carries a plain URL string, while "message"
// events carry JSON-RPC response objects. This type implements json.Marshaler so
// the servicekit JSONCodec passes the bytes through without double-encoding.
type SSEData struct {
	text string          // raw text (e.g., URL)
	json json.RawMessage // pre-encoded JSON (e.g., JSON-RPC response)
}

// SSEText creates an SSEData containing raw text that will not be JSON-encoded.
func SSEText(s string) SSEData { return SSEData{text: s} }

// SSEJSON creates an SSEData containing pre-encoded JSON bytes.
func SSEJSON(j json.RawMessage) SSEData { return SSEData{json: j} }

// sseDataCodec encodes SSEData values for the SSE wire format.
// It returns raw bytes directly — text stays unquoted, JSON passes through.
// This bypasses json.Marshal which would reject non-JSON text or double-encode.
type sseDataCodec struct{}

func (c *sseDataCodec) Encode(msg SSEData) ([]byte, gohttp.MessageType, error) {
	if msg.json != nil {
		return msg.json, gohttp.TextMessage, nil
	}
	return []byte(msg.text), gohttp.TextMessage, nil
}

func (c *sseDataCodec) Decode(data []byte, msgType gohttp.MessageType) (any, error) {
	return data, nil
}


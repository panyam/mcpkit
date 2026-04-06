package mcpkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	gohttp "github.com/panyam/servicekit/http"
)

// sseTransport implements the MCP HTTP+SSE transport (2024-11-05 spec).
// Each SSE connection is an independent MCP session with its own Dispatcher.
type sseTransport struct {
	server          *Server
	hub             *gohttp.SSEHub[SSEData]
	sessions        sync.Map // sessionID → *Dispatcher
	sessionSubjects sync.Map // sessionID → subject string (for principal binding)
	config          transportConfig
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

	dispVal, ok := t.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "session not found or expired", http.StatusGone)
		return
	}
	dispatcher := dispVal.(*Dispatcher)

	// Verify the POST principal matches the session-opening principal.
	// Prevents user B (with a valid token) from posting to user A's session.
	if subj, ok := t.sessionSubjects.Load(sessionID); ok {
		if claims == nil || claims.Subject != subj.(string) {
			http.Error(w, "forbidden: session principal mismatch", http.StatusForbidden)
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		// JSON parse error — push error response on SSE stream
		errResp := NewErrorResponse(json.RawMessage("null"), ErrCodeParse, "parse error: "+err.Error())
		raw, _ := json.Marshal(errResp)
		t.hub.SendEvent(sessionID, "message", SSEJSON(raw))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := t.server.dispatchWith(dispatcher, r.Context(), claims, &req)

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

	if !t.hub.SendEvent(sessionID, "message", SSEJSON(raw)) {
		http.Error(w, "session disconnected", http.StatusGone)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// mcpSSEConn is the per-session SSE connection for MCP.
// It embeds BaseSSEConn[any] and adds session tracking.
type mcpSSEConn struct {
	gohttp.BaseSSEConn[SSEData]
	sessionID string
	transport *sseTransport
	request   *http.Request // stored for building the POST URL
}

func (c *mcpSSEConn) OnStart(w http.ResponseWriter, r *http.Request) error {
	c.request = r
	if err := c.BaseSSEConn.OnStart(w, r); err != nil {
		return err
	}

	// Register in hub and create per-session dispatcher
	c.transport.hub.Register(&c.BaseSSEConn)
	dispatcher := c.transport.server.newSession()

	// Wire up server-to-client notifications via the SSE stream.
	// This closure captures the sessionID and hub, allowing tool handlers
	// to push notifications (logging, progress, etc.) during execution.
	sessionID := c.sessionID
	hub := c.transport.hub
	dispatcher.notifyFunc = func(method string, params any) {
		raw, err := marshalNotification(method, params)
		if err != nil {
			return
		}
		hub.SendEvent(sessionID, "message", SSEJSON(raw))
	}

	c.transport.sessions.Store(c.sessionID, dispatcher)

	// Send endpoint event with the POST URL as raw text.
	// MCP clients expect the SSE data field to be a plain URL, not JSON-encoded.
	// We send it directly through the Writer to bypass the JSONCodec which
	// would add JSON quotes around the string.
	postURL := c.transport.postURL(r) + "?sessionId=" + c.sessionID
	c.SendEvent("endpoint", SSEText(postURL))

	return nil
}

func (c *mcpSSEConn) OnClose() {
	c.transport.hub.Unregister(c.ConnId())
	c.transport.sessions.Delete(c.sessionID)
	c.transport.sessionSubjects.Delete(c.sessionID)
	c.BaseSSEConn.OnClose()
}

// mcpSSEHandler implements gohttp.SSEHandler for MCP session creation.
type mcpSSEHandler struct {
	transport *sseTransport
}

func (h *mcpSSEHandler) Validate(w http.ResponseWriter, r *http.Request) (*mcpSSEConn, bool) {
	// Auth check — prevent unauthenticated session creation
	claims, err := h.transport.server.CheckAuth(r)
	if err != nil {
		writeAuthError(w, err)
		return nil, false
	}

	// Enforce max sessions
	if h.transport.config.maxSessions > 0 && h.transport.hub.Count() >= h.transport.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return nil, false
	}

	sessionID := generateSessionID()

	// Bind the authenticated principal to this session so POST /message
	// can verify the same principal is making requests.
	if claims != nil && claims.Subject != "" {
		h.transport.sessionSubjects.Store(sessionID, claims.Subject)
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

// generateSessionID returns a cryptographically random 32-character hex string.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

package mcpkit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	gohttp "github.com/panyam/servicekit/http"
)

// sseTransport implements the MCP HTTP+SSE transport (2024-11-05 spec).
// Each SSE connection is an independent MCP session with its own Dispatcher.
type sseTransport struct {
	server   *Server
	hub      *gohttp.SSEHub[any]
	sessions sync.Map // sessionID → *Dispatcher
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
		hub:    gohttp.NewSSEHub[any](),
		config: cfg,
	}
}

// handler returns an http.Handler that serves the SSE and message endpoints.
func (t *sseTransport) handler() http.Handler {
	mux := http.NewServeMux()
	prefix := strings.TrimRight(t.config.prefix, "/")

	sseHandler := &mcpSSEHandler{transport: t}
	sseConfig := &gohttp.SSEConnConfig{
		KeepalivePeriod: t.config.keepalivePeriod,
	}

	mux.HandleFunc(prefix+"/sse", gohttp.SSEServe[any](sseHandler, sseConfig))
	mux.HandleFunc(prefix+"/message", t.handleMessage)

	return mux
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
	if err := t.server.CheckAuth(r); err != nil {
		var authErr *AuthError
		if errors.As(err, &authErr) {
			http.Error(w, authErr.Message, authErr.Code)
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
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
		t.hub.SendEvent(sessionID, "message", json.RawMessage(raw))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := t.server.dispatchWith(dispatcher, r.Context(), &req)

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

	if !t.hub.SendEvent(sessionID, "message", json.RawMessage(raw)) {
		http.Error(w, "session disconnected", http.StatusGone)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// mcpSSEConn is the per-session SSE connection for MCP.
// It embeds BaseSSEConn[any] and adds session tracking.
type mcpSSEConn struct {
	gohttp.BaseSSEConn[any]
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
	c.transport.sessions.Store(c.sessionID, c.transport.server.newSession())

	// Send endpoint event with the POST URL.
	// We send the URL as a pre-marshaled json.RawMessage so the JSONCodec
	// passes it through without adding extra quotes. MCP clients read the
	// SSE data field as a raw URL string.
	postURL := c.transport.postURL(r) + "?sessionId=" + c.sessionID
	urlJSON, _ := json.Marshal(postURL)
	c.SendEvent("endpoint", json.RawMessage(urlJSON))

	return nil
}

func (c *mcpSSEConn) OnClose() {
	c.transport.hub.Unregister(c.ConnId())
	c.transport.sessions.Delete(c.sessionID)
	c.BaseSSEConn.OnClose()
}

// mcpSSEHandler implements gohttp.SSEHandler for MCP session creation.
type mcpSSEHandler struct {
	transport *sseTransport
}

func (h *mcpSSEHandler) Validate(w http.ResponseWriter, r *http.Request) (*mcpSSEConn, bool) {
	// Enforce max sessions
	if h.transport.config.maxSessions > 0 && h.transport.hub.Count() >= h.transport.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return nil, false
	}

	sessionID := generateSessionID()
	conn := &mcpSSEConn{
		BaseSSEConn: gohttp.BaseSSEConn[any]{
			Codec:     &gohttp.JSONCodec{},
			ConnIdStr: sessionID,
			NameStr:   "MCP",
		},
		sessionID: sessionID,
		transport: h.transport,
	}
	return conn, true
}

// generateSessionID returns a cryptographically random 32-character hex string.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

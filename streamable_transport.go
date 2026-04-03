package mcpkit

import (
	"encoding/json"
	"errors"
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
func (t *streamableTransport) handleRoot(w http.ResponseWriter, r *http.Request) {
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
		t.handleInitialize(w, r, &req)
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

	// Dispatch
	resp := t.server.dispatchWith(dispatcher, r.Context(), &req)

	// Notification/response (no JSON-RPC response expected)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Request → JSON response
	w.Header().Set("Content-Type", "application/json")
	raw, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Write(raw)
}

// handleInitialize handles POST initialize: creates session, dispatches, returns
// the response with Mcp-Session-Id header.
func (t *streamableTransport) handleInitialize(w http.ResponseWriter, r *http.Request, req *Request) {
	// Enforce max sessions
	if t.config.maxSessions > 0 && t.sessionCount() >= t.config.maxSessions {
		http.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return
	}

	// Create session dispatcher and dispatch initialize
	dispatcher := t.server.newSession()
	resp := t.server.dispatchWith(dispatcher, r.Context(), req)

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

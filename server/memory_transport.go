package server

// In-process transport for the MCP client. Calls Server dispatch directly
// without HTTP, serialization overhead, or network. Useful for:
//   - Unit and integration tests (fast, no port conflicts, race detector works)
//   - Embedded scenarios (client + server in same process)
//   - Benchmarking tool handlers without transport noise
//
// # Testing coverage note
//
// The in-process transport passes *core.Request / *core.Response directly —
// no JSON marshal/unmarshal of the envelope. This means JSON edge cases
// (malformed input, omitempty behavior, RawMessage boundaries) are NOT
// exercised here. Those are covered by the HTTP transport subtests.
//
// The forAllTransports pattern runs every test against all 3 transports
// (Streamable HTTP, SSE, in-process). The split is intentional:
//   - In-process: catches logic bugs (routing, session state, sampling flow)
//   - HTTP transports: catch serialization, SSE framing, auth header, Content-Type
//
// If needed in the future, a WithJSONRoundTrip(true) option could marshal
// and unmarshal the Request/Response to simulate wire behavior without HTTP.
// This was considered and deferred — the HTTP subtests already provide coverage.

import (
	"context"
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
)

// InProcessOption configures an InProcessTransport.
type InProcessOption func(*InProcessTransport)

// WithServerRequestHandler sets a handler for server-to-client requests
// (sampling/createMessage, elicitation/create). When the server sends a request
// during tool execution, this handler is called to produce a response.
func WithServerRequestHandler(h core.ServerRequestHandler) InProcessOption {
	return func(t *InProcessTransport) { t.reqHandler = h }
}

// WithNotificationHandler sets a callback for server-to-client notifications
// (logging, progress, resource updates). Useful in tests to verify notification
// delivery.
func WithNotificationHandler(h core.NotificationHandler) InProcessOption {
	return func(t *InProcessTransport) { t.notifyHandler = h }
}

// InProcessTransport implements core.Transport by dispatching directly to a
// Server in the same process. No HTTP, no serialization of the Request/Response
// envelope — domain params (json.RawMessage) pass through as-is.
type InProcessTransport struct {
	server        *Server
	dispatcher    *Dispatcher
	reqHandler    core.ServerRequestHandler
	notifyHandler core.NotificationHandler
}

// NewInProcessTransport creates a transport that dispatches to the given server
// in-memory. Use with client.WithTransport() to create a client that talks to
// the server without HTTP.
//
// Example:
//
//	srv := server.New(core.ServerInfo{Name: "test", Version: "1.0"})
//	srv.RegisterTool(def, handler)
//	transport := server.NewInProcessTransport(srv,
//	    server.WithServerRequestHandler(mySamplingHandler),
//	)
//	c := client.New("memory://", core.ClientInfo{...}, client.WithTransport(transport))
func NewInProcessTransport(srv *Server, opts ...InProcessOption) *InProcessTransport {
	t := &InProcessTransport{server: srv}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Connect creates a per-session dispatcher and wires notification delivery and
// server-to-client request handling.
func (t *InProcessTransport) Connect(ctx context.Context) error {
	t.dispatcher = t.server.newSession()
	t.dispatcher.sessionID = "memory"

	// Wire notifyFunc for server-to-client notifications.
	t.dispatcher.SetNotifyFunc(func(method string, params any) {
		if t.notifyHandler != nil {
			raw, err := json.Marshal(params)
			if err != nil {
				return
			}
			t.notifyHandler(method, raw)
		}
	})

	// Wire pushRequest for server-to-client requests (sampling, elicitation).
	// The server pushes a JSON-RPC request; we dispatch to the handler and
	// route the response back to the dispatcher's pending map.
	if t.reqHandler != nil {
		t.dispatcher.pushRequest = func(raw json.RawMessage) {
			var req core.Request
			if err := json.Unmarshal(raw, &req); err != nil {
				return
			}
			resp := t.reqHandler(ctx, &req)
			if resp != nil {
				t.dispatcher.RouteResponse(resp)
			}
		}
	}
	return nil
}

// Call dispatches a JSON-RPC request and returns the response.
// Passes *Request directly to the server — no HTTP marshal/unmarshal overhead.
func (t *InProcessTransport) Call(ctx context.Context, req *core.Request) (*core.Response, error) {
	// Build request func from dispatcher's push + pending infrastructure
	var requestFunc core.RequestFunc
	if t.dispatcher.pushRequest != nil {
		requestFunc = t.dispatcher.makeRequestFunc(t.dispatcher.pushRequest)
	}

	resp := t.server.dispatchWithNotifyAndRequest(
		t.dispatcher, ctx, nil,
		t.dispatcher.getNotifyFunc(), requestFunc, req,
	)
	return resp, nil
}

// Notify dispatches a JSON-RPC notification (no response expected).
func (t *InProcessTransport) Notify(ctx context.Context, req *core.Request) error {
	t.server.dispatchWith(t.dispatcher, ctx, nil, req)
	return nil
}

// Close tears down the session.
func (t *InProcessTransport) Close() error {
	if t.dispatcher != nil {
		t.dispatcher.Close()
	}
	return nil
}

// SessionID returns "memory" for the in-process transport.
func (t *InProcessTransport) SessionID() string { return "memory" }

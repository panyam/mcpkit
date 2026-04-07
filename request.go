package mcpkit

// Server-to-client request infrastructure.
//
// MCP allows the server to send JSON-RPC requests to the client (not just
// notifications). The client must respond with a JSON-RPC response containing
// the same request ID. This enables two features:
//
//   - sampling/createMessage — server asks the client's LLM to generate text
//   - elicitation/create — server asks the client to collect user input
//
// # Flow (Sampling Example)
//
// Normal MCP flow is client→server. Sampling/elicitation reverses this:
//
//	┌──────────────┐                    ┌───────────────┐                    ┌──────────────┐
//	│  Tool Handler │                    │   Transport   │                    │    Client    │
//	└──────┬───────┘                    └───────┬───────┘                    └──────┬───────┘
//	       │                                    │                                   │
//	       │  Sample(ctx, req)                  │                                   │
//	       │──────────────────►                 │                                   │
//	       │  1. Check clientCaps.Sampling      │                                   │
//	       │  2. Generate ID "srv-1"            │                                   │
//	       │  3. Register pending chan           │                                   │
//	       │  4. Push JSON-RPC request ────────►│                                   │
//	       │                                    │  SSE event: {"jsonrpc":"2.0",     │
//	       │                                    │   "id":"srv-1",                   │
//	       │                                    │   "method":"sampling/createMessage",
//	       │                                    │   "params":{...}}                 │
//	       │                                    │──────────────────────────────────►│
//	       │                                    │                                   │  Run LLM
//	       │  5. Block on pending chan           │                                   │  inference
//	       │     (with ctx timeout)             │                                   │
//	       │                                    │  POST: {"jsonrpc":"2.0",          │
//	       │                                    │   "id":"srv-1",                   │
//	       │                                    │   "result":{...}}                 │
//	       │                                    │◄──────────────────────────────────│
//	       │                                    │                                   │
//	       │                                    │  6. Detect response (no "method") │
//	       │                                    │  7. RouteResponse → pending chan  │
//	       │  8. Unblock, return result ◄───────│                                   │
//	       │                                    │                                   │
//	       ▼                                    ▼                                   ▼
//
// The same flow applies to elicitation/create, except the client prompts the
// human user for input instead of running LLM inference.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
)

// RequestFunc sends a server-to-client JSON-RPC request and blocks until the
// client sends a response. method is the JSON-RPC method (e.g., "sampling/createMessage").
// params will be JSON-marshaled as the request's params field.
// Returns the raw JSON result on success, or an error on timeout, transport
// failure, or JSON-RPC error from the client.
type RequestFunc func(ctx context.Context, method string, params any) (json.RawMessage, error)

// ErrNoRequestFunc is returned when Sample() or Elicit() is called outside a
// session context where server-to-client requests are not available (e.g., no
// transport wired, or stateless mode).
var ErrNoRequestFunc = errors.New("server-to-client requests not available in this context")

// pendingServerRequest tracks an in-flight server-to-client request awaiting a response.
type pendingServerRequest struct {
	ch chan serverResponse
}

// serverResponse carries the result of a server-to-client request.
type serverResponse struct {
	Result json.RawMessage
	Error  *Error
}

// marshalRequest builds a JSON-RPC 2.0 request with a string ID.
func marshalRequest(id string, method string, params any) (json.RawMessage, error) {
	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	return json.Marshal(req)
}

// sendServerRequest generates a unique request ID, pushes a JSON-RPC request
// to the client via pushFunc, and waits for the client's response. The pending
// map and ID counter are on the Dispatcher. This is the core implementation
// called by the RequestFunc that transports wire onto the Dispatcher.
func sendServerRequest(
	ctx context.Context,
	method string,
	params any,
	nextID *atomic.Int64,
	pending *pendingMap,
	pushFunc func(raw json.RawMessage),
) (json.RawMessage, error) {
	id := fmt.Sprintf("srv-%d", nextID.Add(1))

	raw, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("marshal server request: %w", err)
	}

	// Register pending channel before pushing to avoid race.
	pr := &pendingServerRequest{ch: make(chan serverResponse, 1)}
	pending.Store(id, pr)
	defer pending.Delete(id)

	// Push the request to the client.
	pushFunc(raw)

	// Wait for the client's response or context cancellation.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-pr.ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("client error: [%d] %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// routeServerResponse routes an incoming JSON-RPC response to a pending
// server-to-client request. Returns true if the response was matched to a
// pending request, false if no matching request was found (stale or unknown ID).
func routeServerResponse(pending *pendingMap, resp *Response) bool {
	if resp.ID == nil {
		return false
	}
	// The ID might be a JSON string (quoted) — normalize to bare string.
	var id string
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		// Might be a number — use raw string representation
		id = string(resp.ID)
	}
	val, ok := pending.Load(id)
	if !ok {
		return false
	}
	pr := val.(*pendingServerRequest)
	pr.ch <- serverResponse{Result: resp.Result, Error: resp.Error}
	return true
}

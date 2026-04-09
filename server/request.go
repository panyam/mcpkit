package server

// Server-to-client request implementation.
//
// This file contains the Dispatcher-level machinery for sending JSON-RPC
// requests to clients and routing their responses. The public types
// (RequestFunc, ErrNoRequestFunc) live in core/.
//
// Flow: tool handler calls core.Sample(ctx) → sessionCtx.request → Dispatcher.makeRequestFunc
// → sendServerRequest (this file) → pushFunc (transport-provided) → client responds
// → transport calls Dispatcher.RouteResponse → routeServerResponse (this file) → unblock.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	core "github.com/panyam/mcpkit/core"
)

// PendingMap is a type alias for SyncMap used to track pending server-to-client requests.
// Exported because it's referenced by transport implementations.
type PendingMap = pendingMap

// pendingServerRequest tracks an in-flight server-to-client request awaiting a response.
type pendingServerRequest struct {
	ch chan serverResponse
}

// serverResponse carries the result of a server-to-client request.
type serverResponse struct {
	Result json.RawMessage
	Error  *core.Error
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
// to the client via pushFunc, and waits for the client's response.
func sendServerRequest(
	ctx context.Context,
	method string,
	params any,
	nextID *atomic.Int64,
	pending *PendingMap,
	pushFunc func(raw json.RawMessage),
) (json.RawMessage, error) {
	id := fmt.Sprintf("srv-%d", nextID.Add(1))

	raw, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("marshal server request: %w", err)
	}

	pr := &pendingServerRequest{ch: make(chan serverResponse, 1)}
	pending.Store(id, pr)
	defer pending.Delete(id)

	pushFunc(raw)

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
// server-to-client request. Returns true if matched.
func routeServerResponse(pending *PendingMap, resp *core.Response) bool {
	if resp.ID == nil {
		return false
	}
	var id string
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		id = string(resp.ID)
	}
	pr, ok := pending.Load(id)
	if !ok {
		return false
	}
	pr.ch <- serverResponse{Result: resp.Result, Error: resp.Error}
	return true
}

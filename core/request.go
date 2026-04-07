package core

// Server-to-client request types.
//
// See server/request.go for the implementation (sendServerRequest, routeServerResponse).
// See the flow diagram in server/request.go for the full request lifecycle.

import (
	"context"
	"encoding/json"
	"errors"
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

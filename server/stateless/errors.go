package stateless

import (
	core "github.com/panyam/mcpkit/core"
)

// HTTPStatusForCode maps a SEP-2575 JSON-RPC error code to the HTTP
// status the transport must return. Codes the spec does not pin map
// to 200 (the legacy behavior of "JSON-RPC error in a 200 body").
//
// Transport-aware error mapping is required by SEP-2575: a stateless
// client MAY use the HTTP status as a fast-path signal before parsing
// the JSON body. Specifically:
//
//	-32601 Method not found              → 404 Not Found
//	-32001 HeaderMismatch                → 400 Bad Request
//	-32003 MissingRequiredClientCap      → 400 Bad Request
//	-32004 UnsupportedProtocolVersion    → 400 Bad Request
//	-32602 Invalid params                → 400 Bad Request (missing _meta etc.)
//	-32700 Parse / -32600 InvalidRequest → 400 Bad Request
//	everything else                      → 200 OK (body carries the error)
//
// Notification frames (no id, no error) are out of scope for this
// mapper; the transport applies its own status for those.
func HTTPStatusForCode(code int) int {
	switch code {
	case core.ErrCodeMethodNotFound:
		return 404
	case core.ErrCodeHeaderMismatch,
		core.ErrCodeMissingRequiredClientCapability,
		core.ErrCodeUnsupportedProtocolVersion,
		core.ErrCodeInvalidParams,
		core.ErrCodeParse,
		core.ErrCodeInvalidRequest:
		return 400
	default:
		return 200
	}
}

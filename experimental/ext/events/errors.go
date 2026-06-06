package events

import (
	"encoding/json"

	"github.com/panyam/mcpkit/core"
)

// JSON-RPC error codes used by the MCP Events extension.
//
// These five codes are intentionally general-purpose. The MCP Events
// spec consolidated its error surface on 2026-05-22 (design-sketch
// commit 567be29): seven events-specific codes collapsed into five
// reusable codes carrying typed `data` discriminators. The spec frames
// the new set as "candidates for promotion to the base MCP error
// registry" with the directive that "future SEPs SHOULD reuse them
// rather than introduce overlapping codes."
//
// The codes live here pending that promotion. Other extensions in
// this repo (or downstream SEPs) should feel free to reuse these
// constants rather than mint new ones in the same JSON-RPC server
// range [-32000, -32099].
//
// Spec-defined codes (full set):
//
//	-32011 NotFound              — Unknown event or subscription (discriminate via data.kind)
//	-32012 Forbidden             — Caller lacks permission for this operation
//	-32013 ResourceExhausted     — Server-imposed limit reached (e.g., subscription cap)
//	-32014 Unsupported           — Requested feature/value combination is unsupported
//	-32015 CallbackEndpointError — Webhook endpoint is unreachable or rejected the request
//
// Each emission site SHOULD attach the matching typed `data` payload so
// clients can branch on machine-readable discriminators without
// string-matching the human-readable `message` field. The helpers
// further down (newNotFoundError, newForbiddenError, …) enforce this:
// they wrap core.NewErrorResponseWithData with the right struct shape.
const (
	ErrCodeNotFound              = -32011
	ErrCodeForbidden             = -32012
	ErrCodeResourceExhausted     = -32013
	ErrCodeUnsupported           = -32014
	ErrCodeCallbackEndpointError = -32015
)

// NotFoundData is the typed `data` payload attached to -32011 NotFound
// responses. The Kind discriminator tells the client what was missing
// without parsing the human-readable message — "event" for an unknown
// event name on poll/subscribe/stream, "subscription" for an
// unsubscribe target that doesn't exist.
type NotFoundData struct {
	Kind string `json:"kind"` // "event" | "subscription"
}

// ResourceExhaustedData is the typed `data` payload attached to -32013
// ResourceExhausted responses. Limit names the exhausted resource
// (e.g., "subscriptions"); Max carries the configured ceiling when the
// server is willing to expose it. Max is omitted when zero so the
// client can distinguish "limit hit, ceiling not disclosed" from
// "limit hit, ceiling = 0".
type ResourceExhaustedData struct {
	Limit string `json:"limit"`
	Max   int64  `json:"max,omitempty"`
}

// UnsupportedData is the typed `data` payload attached to -32014
// Unsupported responses. Feature names the dimension the server is
// rejecting on (e.g., "deliveryMode"); Value carries the specific
// rejected input ("push", "webhook") when one applies.
type UnsupportedData struct {
	Feature string `json:"feature"`
	Value   string `json:"value,omitempty"`
}

// CallbackEndpointErrorData is the typed `data` payload attached to
// -32015 CallbackEndpointError responses. Reason mirrors the
// DeliveryErrorBucket categorical set used on the runtime delivery
// side, so a client gets the same vocabulary for subscribe-time
// validation failures and subsequent delivery failures.
type CallbackEndpointErrorData struct {
	Reason string `json:"reason"` // "connection_refused" | "timeout" | "tls_error" | "http_4xx" | "http_5xx" | "challenge_failed"
}

// newNotFoundError returns a -32011 response with a typed
// NotFoundData{Kind: kind} payload. Use kind "event" when the named
// event source is not registered; use "subscription" when an
// unsubscribe target cannot be located.
func newNotFoundError(id json.RawMessage, kind, message string) *core.Response {
	return core.NewErrorResponseWithData(id, ErrCodeNotFound, message, NotFoundData{Kind: kind})
}

// newForbiddenError returns a -32012 response. Forbidden has no
// machine-readable discriminators today; the helper exists so emission
// sites stay symmetrical with the other typed-data helpers and so a
// future spec revision can add a data shape in one place.
func newForbiddenError(id json.RawMessage, message string) *core.Response {
	return core.NewErrorResponse(id, ErrCodeForbidden, message)
}

// newResourceExhaustedError returns a -32013 response with a typed
// ResourceExhaustedData payload. Pass max=0 when the configured
// ceiling is not exposed; the field is omitted in that case.
func newResourceExhaustedError(id json.RawMessage, limit string, max int64, message string) *core.Response {
	return core.NewErrorResponseWithData(id, ErrCodeResourceExhausted, message,
		ResourceExhaustedData{Limit: limit, Max: max})
}

// newUnsupportedError returns a -32014 response with a typed
// UnsupportedData payload. Use empty value when the feature itself is
// unsupported regardless of the input.
func newUnsupportedError(id json.RawMessage, feature, value, message string) *core.Response {
	return core.NewErrorResponseWithData(id, ErrCodeUnsupported, message,
		UnsupportedData{Feature: feature, Value: value})
}

// newCallbackEndpointError returns a -32015 response with a typed
// CallbackEndpointErrorData payload. The reason MUST be drawn from the
// DeliveryErrorBucket vocabulary so client decoders can use one switch
// across both subscribe-time and delivery-time failures.
func newCallbackEndpointError(id json.RawMessage, reason, message string) *core.Response {
	return core.NewErrorResponseWithData(id, ErrCodeCallbackEndpointError, message,
		CallbackEndpointErrorData{Reason: reason})
}

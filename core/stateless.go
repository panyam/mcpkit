package core

import (
	"encoding/json"
	"fmt"
)

// SEP-2575 stateless wire — types and constants.
//
// The stateless wire is a re-architecture that replaces MCP's `initialize`
// handshake with a per-request _meta envelope, a `server/discover` RPC, a
// `subscriptions/listen` RPC, and specific HTTP status mappings for new
// error codes. Servers may serve it alongside the legacy wire (Dual mode);
// see server.WithStatelessMode and server.DefaultStatelessMode.
//
// Spec: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575

// SupportedStatelessVersions enumerates the protocol versions this build
// speaks on the stateless wire. Returned from server/discover.
//
// Distinct from the legacy supportedProtocolVersions list (which the
// initialize handshake negotiates) — the two are advertised independently.
// The version string itself, DraftProtocolVersion2026V1, lives in
// core/protocol.go since it's the draft series identifier shared across
// every draft-targeted SEP (2243, 2549, 2356, 2567, 2575, 2663, ...).
var SupportedStatelessVersions = []string{DraftProtocolVersion2026V1}

// SEP-2575 _meta envelope keys. Carried inside the params._meta object on
// every stateless request; missing any of the three triggers -32602.
const (
	MetaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"

	// MetaKeyLogLevel is the per-request log-level opt-in. Absent ⇒ no
	// notifications/message frames may be emitted for this request.
	MetaKeyLogLevel = "io.modelcontextprotocol/logLevel"

	// MetaKeySubscriptionID is set by the server on every notification
	// frame dispatched over an open subscriptions/listen stream so the
	// client can route frames to the correct subscription.
	MetaKeySubscriptionID = "io.modelcontextprotocol/subscriptionId"
)

// HTTPProtocolVersionHeader is the HTTP header carrying the protocol version
// on every stateless request. Its value MUST equal _meta[MetaKeyProtocolVersion].
const HTTPProtocolVersionHeader = "MCP-Protocol-Version"

// RequestMeta is the SEP-2575 per-request envelope. Every stateless request
// MUST carry one in params._meta.
//
// Decoded via DecodeRequestMeta from a raw params blob; a missing or
// malformed envelope produces a typed *MetaValidationError that the
// dispatcher translates into a -32602 / HTTP 400 response.
type RequestMeta struct {
	ProtocolVersion    string             `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         *ClientInfo        `json:"io.modelcontextprotocol/clientInfo"`
	ClientCapabilities *ClientCapabilities `json:"io.modelcontextprotocol/clientCapabilities"`

	// LogLevel is set when the client opts in to log frames for this
	// request via _meta[MetaKeyLogLevel]. Empty means no logging.
	LogLevel string `json:"io.modelcontextprotocol/logLevel,omitempty"`
}

// MetaValidationError is returned by DecodeRequestMeta when the envelope is
// missing or its required sub-fields are absent. Translates to JSON-RPC
// -32602 + HTTP 400 at the transport boundary.
type MetaValidationError struct {
	// Field names the missing/invalid envelope component for diagnostics:
	// "_meta", "protocolVersion", "clientInfo", or "clientCapabilities".
	Field string
}

func (e *MetaValidationError) Error() string {
	if e.Field == "_meta" {
		return "request params missing required _meta envelope"
	}
	return fmt.Sprintf("request _meta missing required field %q", e.Field)
}

// DecodeRequestMeta extracts the SEP-2575 _meta envelope from raw params.
// Returns a typed *MetaValidationError when the envelope is absent or any
// required sub-field (protocolVersion, clientInfo, clientCapabilities) is
// missing; the dispatcher maps these to -32602 / HTTP 400.
//
// An absent params (empty raw) is treated as "missing _meta" — the wire
// requires _meta on every stateless request.
func DecodeRequestMeta(rawParams json.RawMessage) (*RequestMeta, error) {
	if len(rawParams) == 0 {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	var probe struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(rawParams, &probe); err != nil {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	if len(probe.Meta) == 0 || string(probe.Meta) == "null" {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	var meta RequestMeta
	if err := json.Unmarshal(probe.Meta, &meta); err != nil {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	if meta.ProtocolVersion == "" {
		return nil, &MetaValidationError{Field: "protocolVersion"}
	}
	if meta.ClientInfo == nil {
		return nil, &MetaValidationError{Field: "clientInfo"}
	}
	if meta.ClientCapabilities == nil {
		return nil, &MetaValidationError{Field: "clientCapabilities"}
	}
	return &meta, nil
}

// UnsupportedProtocolVersionData is the structured error payload returned
// when a stateless request advertises a protocol version this server does
// not implement. Supported lists the server's full SupportedStatelessVersions
// so the client can pick a fallback; Requested echoes the version string
// the client sent for diagnostics.
//
// HTTP status: 400. JSON-RPC code: ErrCodeUnsupportedProtocolVersion (-32004).
type UnsupportedProtocolVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}

// MissingRequiredClientCapabilityData is the structured error payload for
// ErrCodeMissingRequiredClientCapability (-32021). RequiredCapabilities
// mirrors the ClientCapabilities shape — the server returns the same
// capability object it expects the client to declare, so the client can
// merge it into its next request's _meta[MetaKeyClientCapabilities] and
// retry. Example: {"elicitation": {}} for a tool that requires elicitation.
//
// HTTP status: 400. JSON-RPC code: -32021.
//
// Wire-shape note: the SEP-2575 conformance scenario as of this writing
// checks for a string-array shape (Array.isArray + .includes("sampling")),
// which contradicts the schema's object shape exemplified at
// schema/draft/examples/MissingRequiredClientCapabilityError/. We emit
// the schema-correct object shape; an upstream conformance follow-up is
// tracked to align the test.
type MissingRequiredClientCapabilityData struct {
	RequiredCapabilities ClientCapabilities `json:"requiredCapabilities"`
}

// HeaderMismatch payload note: the structured data for ErrCodeHeaderMismatch
// (-32020) is a generic {reason, header, expected, received, ...} map built
// by the SEP-2243 path (server/header_validation.go) — shared by both the
// SEP-2243 routing-header check (Mcp-Method/Mcp-Name) and the SEP-2575
// version-header cross-check (MCP-Protocol-Version vs _meta protocolVersion).
// Conformance test for SEP-2575 only checks status code + JSON-RPC code,
// not the payload shape, so the shared map suffices for both surfaces.

// MissingCapabilityError is a typed error tool/resource/prompt handlers
// return when the per-request _meta.clientCapabilities does not declare
// a capability the handler needs. The stateless dispatcher detects the
// type via errors.As at the tools/call boundary and translates it into
// a JSON-RPC -32021 response carrying MissingRequiredClientCapabilityData.
//
// Usage from inside a tool handler under the SEP-2575 wire:
//
//	if meta.ClientCapabilities.Sampling == nil {
//	    return core.ToolResult{}, &core.MissingCapabilityError{
//	        Required: core.ClientCapabilities{Sampling: &struct{}{}},
//	        Message:  "this tool requires the sampling capability",
//	    }
//	}
//
// Required mirrors the shape of ClientCapabilities so the client can merge
// it into the next request's _meta envelope and retry without guessing.
type MissingCapabilityError struct {
	Required ClientCapabilities
	Message  string
}

func (e *MissingCapabilityError) Error() string {
	if e.Message == "" {
		return "server requires a client capability the client did not declare"
	}
	return e.Message
}

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
// every stateless request; a missing protocolVersion or clientCapabilities
// triggers -32602. clientInfo is a SHOULD since spec PR 3002 — servers MUST
// serve requests that omit it.
const (
	MetaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"

	// MetaKeyServerInfo is the result-side identity field (spec PR 3002).
	// Servers SHOULD set it in every result's _meta so clients retain the
	// server identity that the initialize handshake used to carry before
	// SEP-2575 removed it. Self-reported and unverified — for display,
	// logging, and debugging only; never for behavior or security decisions.
	MetaKeyServerInfo = "io.modelcontextprotocol/serverInfo"

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
	ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`

	// ClientInfo identifies the client software. Optional since spec
	// PR 3002 (clients SHOULD send it; servers MUST NOT require it) —
	// nil when the request omitted the field. Self-reported and
	// unverified: display/logging/debugging only.
	ClientInfo         *ClientInfo         `json:"io.modelcontextprotocol/clientInfo"`
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
	// "_meta", "protocolVersion", or "clientCapabilities".
	Field string
}

func (e *MetaValidationError) Error() string {
	if e.Field == "_meta" {
		return "request params missing required _meta envelope"
	}
	return fmt.Sprintf("request _meta missing required field %q", e.Field)
}

// DecodeRequestMeta extracts the SEP-2575 _meta envelope from raw params.
// Returns a typed *MetaValidationError when the envelope is absent or a
// required sub-field (protocolVersion, clientCapabilities) is missing; the
// dispatcher maps these to -32602 / HTTP 400. clientInfo is not required
// (spec PR 3002) — RequestMeta.ClientInfo is nil when the request omits it.
//
// An absent params (empty raw) is treated as "missing _meta" — the wire
// requires _meta on every stateless request.
func DecodeRequestMeta(rawParams json.RawMessage) (*RequestMeta, error) {
	m := NewRawJSON(rawParams)
	return DecodeRequestMetaFromRawJSON(&m)
}

// DecodeRequestMetaFromRawJSON is the RawJSON form of DecodeRequestMeta
// (issue 733). It reads the SEP-2575 per-request `_meta` envelope through the
// message's cached, spine-free Meta() — so on a request whose metadata is also
// read elsewhere (trace middleware) the params are scanned once, and a large
// `arguments` sibling is never copied. Prefer this via &req.Params on the
// dispatch path.
func DecodeRequestMetaFromRawJSON(m *RawJSON) (*RequestMeta, error) {
	meta, ok := m.Meta()
	if !ok {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	var rm RequestMeta
	if err := meta.Bind(&rm); err != nil {
		return nil, &MetaValidationError{Field: "_meta"}
	}
	if rm.ProtocolVersion == "" {
		return nil, &MetaValidationError{Field: "protocolVersion"}
	}
	if rm.ClientCapabilities == nil {
		return nil, &MetaValidationError{Field: "clientCapabilities"}
	}
	return &rm, nil
}

// ResultMeta is the SEP-2575 result-side _meta shape (spec PR 3002's
// ResultMetaObject). Currently carries only the optional server identity;
// decode a result's _meta into this to read it.
type ResultMeta struct {
	// ServerInfo identifies the server software that produced the
	// response. Servers SHOULD set it on every result. Self-reported
	// and unverified — display/logging/debugging only; clients SHOULD
	// NOT use it to change behavior or for security decisions.
	ServerInfo *ServerInfo `json:"io.modelcontextprotocol/serverInfo,omitempty"`
}

// InjectServerInfoIntoResult returns result with
// _meta[MetaKeyServerInfo] set to info, implementing the spec PR 3002
// SHOULD that servers identify themselves on every response. Existing
// _meta keys are preserved, and a caller-set serverInfo wins (mirroring
// the InjectTraceContextIntoParams convention). Returns the input
// unchanged when result is not a JSON object (no MCP result is), when
// info is empty, or on any marshal failure — stamping is best-effort
// decoration, never a dispatch error.
//
// The returned value is a json.RawMessage when stamping occurred; the
// transport serializes it verbatim.
func InjectServerInfoIntoResult(result any, info ServerInfo) any {
	if info.Name == "" && info.Version == "" {
		return result
	}
	raw, err := MarshalJSON(result)
	if err != nil {
		return result
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return result
	}
	meta := map[string]json.RawMessage{}
	if existing, ok := obj["_meta"]; ok {
		if err := json.Unmarshal(existing, &meta); err != nil {
			return result
		}
		if _, present := meta[MetaKeyServerInfo]; present {
			return result
		}
	}
	infoRaw, err := json.Marshal(info)
	if err != nil {
		return result
	}
	meta[MetaKeyServerInfo] = infoRaw
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return result
	}
	obj["_meta"] = metaRaw
	out, err := json.Marshal(obj)
	if err != nil {
		return result
	}
	return json.RawMessage(out)
}

// UnsupportedProtocolVersionData is the structured error payload returned
// when a stateless request advertises a protocol version this server does
// not implement. Supported lists the server's full SupportedStatelessVersions
// so the client can pick a fallback; Requested echoes the version string
// the client sent for diagnostics.
//
// HTTP status: 400. JSON-RPC code: ErrCodeUnsupportedProtocolVersion (-32022).
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

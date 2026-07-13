package core

import (
	"context"
	"encoding/json"
)

// W3C Baggage — contract surface, sibling to TraceContext.
//
// This file mirrors core/trace.go for the W3C Baggage standard
// (https://www.w3.org/TR/baggage/). Baggage is a separate W3C standard
// from Trace Context (`traceparent` / `tracestate`), even though both
// commonly travel together on the wire — see SEP-2028's "Predefined
// Groups" section for the upstream rationale (trace-context and
// baggage are versioned independently and configured separately).
//
// What this file does:
//   - Defines the opaque core.Baggage type plus the bare W3C wire-key
//     constant (MetaKeyBaggage = "baggage").
//   - Extract/Inject helpers symmetric to TraceContext for both
//     map[string]any envelopes (`_meta`) and json.RawMessage params.
//   - Context plumbing (WithBaggage / BaggageFromContext) so handler
//     code can read the propagated baggage without re-parsing _meta.
//
// What this file does NOT do:
//   - Parse the comma-separated key=value list. Baggage is opaque to
//     mcpkit core — adapters (ext/otel) consume the raw string and
//     feed it to OTel's propagator, which already implements the W3C
//     parsing rules.
//   - Validate semantic content. Structural validation (printable
//     ASCII, control-char rejection, size cap) protects against
//     header-injection / amplification per SEP-2028 §3.1; deeper
//     parsing belongs in adapters.
//   - Emit any spans (the server/client trace middleware widens to
//     consume baggage in the same PR; this file is the contract).
//
// Spec references:
//   - W3C Baggage: https://www.w3.org/TR/baggage/
//   - SEP-2028 (upstream PR, mcpkit issue 739): the proposed
//     `baggage` predefined group; mcpkit ships the propagation
//     surface independently of the SEP advancing.

// MetaKeyBaggage is the key under which the W3C Baggage list is
// carried inside an MCP request's `_meta` envelope. Bare W3C name,
// not under the `io.modelcontextprotocol/` namespace — same rule
// MetaKeyTraceparent / MetaKeyTracestate follow, because the W3C
// spec defines the field, not MCP.
const MetaKeyBaggage = "baggage"

// maxBaggageLength caps an individual baggage value to defend
// against the amplification attack SEP-2028 §"Security Implications"
// flags. The SEP suggests an 8KB cap on the total `_meta` payload;
// applying the same cap per-field for baggage keeps a single
// well-formed but oversized list from dominating the envelope.
//
// The W3C Baggage spec itself does not mandate a length cap, so this
// value is a defensive choice — generous enough to admit realistic
// vendor extensions (Datadog / Honeycomb / Lightstep usually fit in
// <1KB) but small enough that a runaway producer is caught.
const maxBaggageLength = 8192

// Baggage is the propagated W3C Baggage list for a single MCP
// request. The value is opaque to mcpkit core — it carries the same
// shape that lands on the HTTP `Baggage` header and the same shape
// the OTel SDK's propagator consumes. mcpkit-side validation is
// structural only (see isValidBaggage); semantic parsing happens in
// adapters.
//
// A zero Baggage ("") means "no baggage active." Callers must treat
// it as a valid absence, not an error. Sibling to TraceContext but
// kept as a distinct type so signatures stay precise about which
// W3C standard they propagate.
type Baggage string

// IsZero reports whether b carries no baggage at all. Provided so
// call sites read as a single branch ("if b.IsZero() { ... }").
func (b Baggage) IsZero() bool {
	return b == ""
}

// --- W3C Baggage extraction / injection --------------------------------------

// ExtractBaggage reads the W3C `baggage` field from an MCP `_meta`
// map. Returns a zero Baggage when the key is absent, when the value
// is not a string, when the value fails structural validation, or
// when the value exceeds maxBaggageLength. Mirror of
// ExtractTraceContext — same defensive contract: malformed input
// never panics, never propagates.
func ExtractBaggage(meta map[string]any) Baggage {
	if meta == nil {
		return ""
	}
	raw, _ := meta[MetaKeyBaggage].(string)
	if !isValidBaggage(raw) {
		return ""
	}
	return Baggage(raw)
}

// InjectBaggage writes the W3C `baggage` field into an MCP `_meta`
// map. Empty Baggage is NOT written — `_meta` stays clean. A nil
// meta panics; callers MUST provide a non-nil map (mirrors
// InjectTraceContext).
//
// Idempotent: calling InjectBaggage twice with the same b leaves
// meta in the same end state.
func InjectBaggage(meta map[string]any, b Baggage) {
	if b == "" {
		return
	}
	meta[MetaKeyBaggage] = string(b)
}

// ExtractBaggageFromParams reads the W3C `baggage` field out of a
// JSON-RPC request's raw `params` envelope by parsing the `_meta`
// object inside. Returns a zero Baggage when params is nil / not a
// JSON object / `_meta` is absent or non-object, or when the value
// fails the same structural validation as ExtractBaggage. Mirror of
// ExtractTraceContextFromParams.
func ExtractBaggageFromParams(params json.RawMessage) Baggage {
	m := NewRawJSON(params)
	return ExtractBaggageFromRawJSON(&m)
}

// ExtractBaggageFromRawJSON is the RawJSON form of ExtractBaggageFromParams
// (issue 733) — reads `_meta.baggage` through the message's parse-once spine so
// it shares the parse of params with the trace-context and tracelink readers in
// the trace middleware.
func ExtractBaggageFromRawJSON(m *RawJSON) Baggage {
	meta, ok := m.Meta()
	if !ok {
		return ""
	}
	var metaMap map[string]any
	if err := meta.Bind(&metaMap); err != nil {
		return ""
	}
	return ExtractBaggage(metaMap)
}

// InjectBaggageIntoParams returns a params value with
// `_meta.baggage` populated from b. Contract mirrors
// InjectTraceContextIntoParams:
//
//   - If b is zero, params is returned unchanged.
//   - If params is nil, a fresh map with just `_meta` is returned.
//   - If params marshals to a JSON object, `_meta` is read/created
//     and the baggage key is set. Existing entries are preserved
//     (the injection never clobbers — explicit caller-set values
//     win).
//   - If params marshals but is not a JSON object (positional array,
//     scalar, etc.), the value is returned unchanged.
//   - If params fails to marshal, it is returned unchanged so the
//     downstream encoder can surface the original error.
//
// Used by SEP-414 P2 / P3 trace middleware extensions to relay
// baggage alongside `traceparent` / `tracestate` on every outbound
// MCP message. Caller-set values win on both wires.
func InjectBaggageIntoParams(params any, b Baggage) any {
	if b.IsZero() {
		return params
	}
	if params == nil {
		return map[string]any{"_meta": map[string]any{MetaKeyBaggage: string(b)}}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return params
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return params
	}
	if obj == nil {
		obj = map[string]any{}
	}
	meta, _ := obj["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, exists := meta[MetaKeyBaggage]; !exists {
		meta[MetaKeyBaggage] = string(b)
	}
	obj["_meta"] = meta
	return obj
}

// isValidBaggage enforces a structural check on the raw baggage
// value before it propagates. The W3C Baggage spec defines a
// comma-separated list of `key=value;property` triples, but parsing
// the list belongs in adapters (the OTel propagator already does
// it). This function only protects against header-injection /
// amplification:
//
//   - Length cap (maxBaggageLength) — bounds amplification.
//   - Printable ASCII + spaces only — rejects control characters
//     and newlines so an attacker cannot inject extra HTTP headers
//     via embedded CRLF when the value travels onto an outbound
//     HTTP request via HTTPForwardTransport.
//
// Aligns with SEP-2028 §3.1 "Validation Rules" applied symmetrically
// on the inbound side.
func isValidBaggage(s string) bool {
	if s == "" {
		return false
	}
	if len(s) > maxBaggageLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Allow visible ASCII (0x21-0x7E) + space (0x20). Reject
		// everything else — control chars, high-bit chars, etc.
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// --- context.Context plumbing ------------------------------------------------

type baggageCtxKey struct{}

// WithBaggage returns a derived context carrying b. The dispatch
// layer (server trace middleware) calls this after extracting the
// inbound `_meta.baggage` field (or the SEP-2028 HTTP `Baggage`
// header bridge) so handlers can read the active baggage via
// BaseContext.Baggage() or BaggageFromContext.
//
// A zero b is stored as-is so downstream calls observe an explicit
// "no baggage on this request" signal rather than falling through to
// whatever ctx may have carried before. Use this to scrub an
// inherited baggage value at a boundary.
func WithBaggage(ctx context.Context, b Baggage) context.Context {
	return context.WithValue(ctx, baggageCtxKey{}, b)
}

// BaggageFromContext returns the Baggage carried by ctx, or a zero
// Baggage when none has been attached. Always safe to call; never
// panics. Use Baggage.IsZero to test for absence.
//
// The HTTPForwardTransport reads this to decide whether to stamp
// the `Baggage` HTTP header on outbound tool-handler HTTP calls; the
// server / client trace middleware reads it to relay baggage onto
// outbound MCP messages.
func BaggageFromContext(ctx context.Context) Baggage {
	b, _ := ctx.Value(baggageCtxKey{}).(Baggage)
	return b
}

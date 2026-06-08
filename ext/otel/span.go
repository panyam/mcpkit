package otel

import (
	"sync/atomic"

	core "github.com/panyam/mcpkit/core"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Span implements core.Span by delegating to an underlying
// go.opentelemetry.io/otel/trace.Span. The wrapper exists for two
// reasons: to narrow OTel's broader Span surface to the three-method
// contract mcpkit middleware consumes, and to enforce the "End is
// exactly-once" invariant from core/trace.go even though OTel's
// implementations log a noisy warning rather than treating double-End as
// a contract violation.
//
// Safe for concurrent SetAttribute / RecordError calls (the underlying
// OTel SDK Span is thread-safe). End uses a CAS to guarantee the
// underlying End() runs at most once.
type Span struct {
	otel  oteltrace.Span
	ended atomic.Bool
}

// End closes the span. Subsequent calls are no-ops per the core.Span
// contract — the wrapper short-circuits a second call before reaching
// the OTel SDK, preventing the SDK's "span already ended" log warning
// from firing in test loops or unusual handler unwinding paths.
func (s *Span) End() {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
	s.otel.End()
}

// SetAttribute records a string-valued attribute on the underlying OTel
// span. No-op after End. Mirrors the core.Span contract; numeric and
// boolean attributes are outside the P1 surface — callers stringify
// upstream.
func (s *Span) SetAttribute(k, v string) {
	if s.ended.Load() {
		return
	}
	s.otel.SetAttributes(attribute.String(k, v))
}

// RecordError attaches err to the span via OTel's RecordError (which
// emits an "exception" event) AND sets the span status to codes.Error.
// Mirrors the OTel idiom that an unhandled error should both surface as
// an event and degrade the span's status — observability backends use
// Status.Code to filter / count error spans.
//
// Nil err is a no-op (matches the core.Span contract).
func (s *Span) RecordError(err error) {
	if err == nil || s.ended.Load() {
		return
	}
	s.otel.RecordError(err)
	s.otel.SetStatus(codes.Error, err.Error())
}

// AddLink attaches a causal Link to the span mid-flight. Delegates to
// the underlying OTel SDK Span.AddLink (available since OTel Go v1.30).
// No-op after End, matching the contract on every other Span method
// — the OTel SDK's "AddLink must happen before the span is read"
// guarantee is satisfied by the ended-guard here PLUS the underlying
// SDK's own enforcement.
//
// Invalid Link entries (zero or malformed TraceContext) are silently
// dropped, matching the core.Span.AddLink contract — defensive call
// sites can build Link slices from raw inputs without pre-filtering.
//
// Per-link Attributes flow through as attribute.String entries on the
// OTel link, where observability backends render them as link
// metadata (Jaeger / Tempo / Honeycomb show these specially in their
// link UI panels, separate from span attributes).
func (s *Span) AddLink(link core.Link) {
	if s.ended.Load() {
		return
	}
	otelLink, ok := linkToOTelLink(link)
	if !ok {
		return
	}
	s.otel.AddLink(otelLink)
}

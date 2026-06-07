package otel

import (
	"sync/atomic"

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

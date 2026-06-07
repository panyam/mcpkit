package core

import (
	"context"
	"testing"
)

// These tests pin BaseContext.TraceContext()'s contract: the accessor
// reads from the same context.Context plumbing as
// TraceContextFromContext, with no special-casing per handler type. The
// per-handler types (ToolContext, PromptContext, ResourceContext,
// MethodContext) all embed BaseContext, so the same accessor surfaces on
// every handler signature.

func TestBaseContext_TraceContext_Absent(t *testing.T) {
	bc := BaseContext{Context: context.Background()}
	if got := bc.TraceContext(); !got.IsZero() {
		t.Fatalf("absent: expected zero TraceContext, got %+v", got)
	}
}

func TestBaseContext_TraceContext_FromCtxValue(t *testing.T) {
	want := TraceContext{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:  "vendor=x",
	}
	ctx := WithTraceContext(context.Background(), want)
	bc := BaseContext{Context: ctx}
	if got := bc.TraceContext(); got != want {
		t.Fatalf("ctx-value path: got %+v, want %+v", got, want)
	}
}

func TestBaseContext_TraceContext_SurfacesOnToolContext(t *testing.T) {
	// ToolContext embeds BaseContext, so the accessor is reachable
	// through the typed handler context. Pin that so a future refactor
	// of ToolContext doesn't accidentally shadow it.
	want := TraceContext{
		Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	ctx := WithTraceContext(context.Background(), want)
	tc := ToolContext{BaseContext: BaseContext{Context: ctx}}
	if got := tc.TraceContext(); got != want {
		t.Fatalf("ToolContext path: got %+v, want %+v", got, want)
	}
}

func TestBaseContext_TraceContext_SurfacesOnPromptContext(t *testing.T) {
	want := TraceContext{
		Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	ctx := WithTraceContext(context.Background(), want)
	pc := PromptContext{BaseContext: BaseContext{Context: ctx}}
	if got := pc.TraceContext(); got != want {
		t.Fatalf("PromptContext path: got %+v, want %+v", got, want)
	}
}

func TestBaseContext_TraceContext_SurvivesDetach(t *testing.T) {
	// DetachFromClient wraps the context with context.WithoutCancel; the
	// trace context value must propagate through because OTel parentage
	// outlives the inbound HTTP request.
	want := TraceContext{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	ctx := WithTraceContext(context.Background(), want)
	bc := BaseContext{Context: ctx}
	detached := bc.DetachFromClient()
	if got := detached.TraceContext(); got != want {
		t.Fatalf("detached: got %+v, want %+v", got, want)
	}
}

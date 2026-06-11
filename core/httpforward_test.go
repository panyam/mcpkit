package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// validTraceparent is a W3C version-00 traceparent value used across
// the suite. Lowercase hex, non-zero trace-id and span-id.
const validTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

// captureTransport records the most-recent request it received plus
// echoes back a 200 — so tests can read req.Header without
// depending on a real http.Server.
type captureTransport struct {
	lastReq *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.lastReq = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// --- behavior tests ---------------------------------------------------------

func TestHTTPForward_NoContext_PassesThrough(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.invalid", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := cap.lastReq.Header.Get(core.HTTPHeaderTraceparent); got != "" {
		t.Fatalf("no ctx → no Traceparent; got %q", got)
	}
	if got := cap.lastReq.Header.Get(core.HTTPHeaderBaggage); got != "" {
		t.Fatalf("no ctx → no Baggage; got %q", got)
	}
}

func TestHTTPForward_TraceContext_StampsTraceparentAndTracestate(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: validTraceparent,
		Tracestate:  "vendor=value",
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := cap.lastReq.Header.Get(core.HTTPHeaderTraceparent); got != validTraceparent {
		t.Fatalf("Traceparent: got %q, want %q", got, validTraceparent)
	}
	if got := cap.lastReq.Header.Get(core.HTTPHeaderTracestate); got != "vendor=value" {
		t.Fatalf("Tracestate: got %q, want %q", got, "vendor=value")
	}
}

func TestHTTPForward_OnlyTraceparent_NoTracestate(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: validTraceparent,
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := cap.lastReq.Header.Get(core.HTTPHeaderTraceparent); got != validTraceparent {
		t.Fatalf("Traceparent: got %q, want %q", got, validTraceparent)
	}
	if got := cap.lastReq.Header.Get(core.HTTPHeaderTracestate); got != "" {
		t.Fatalf("empty Tracestate must not write the header; got %q", got)
	}
}

func TestHTTPForward_Baggage_StampsHeader(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithBaggage(context.Background(), "userId=alice,tenant=acme")
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := cap.lastReq.Header.Get(core.HTTPHeaderBaggage); got != "userId=alice,tenant=acme" {
		t.Fatalf("Baggage: got %q, want %q", got, "userId=alice,tenant=acme")
	}
}

func TestHTTPForward_TraceAndBaggage_StampsBoth(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: validTraceparent,
		Tracestate:  "vendor=v",
	})
	ctx = core.WithBaggage(ctx, "userId=alice")
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if cap.lastReq.Header.Get(core.HTTPHeaderTraceparent) != validTraceparent {
		t.Fatalf("Traceparent missing")
	}
	if cap.lastReq.Header.Get(core.HTTPHeaderTracestate) != "vendor=v" {
		t.Fatalf("Tracestate missing")
	}
	if cap.lastReq.Header.Get(core.HTTPHeaderBaggage) != "userId=alice" {
		t.Fatalf("Baggage missing")
	}
}

// --- caller-set wins --------------------------------------------------------

func TestHTTPForward_CallerSetTraceparent_Preserved(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: validTraceparent,
	})
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	req.Header.Set(core.HTTPHeaderTraceparent, "00-cafecafecafecafecafecafecafecafe-1234567890abcdef-01")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Caller's explicit value must survive — the wrap never clobbers
	// explicit intent. Same precedence rule the MCP-wire
	// InjectTraceContextIntoParams enforces.
	if got := cap.lastReq.Header.Get(core.HTTPHeaderTraceparent); got != "00-cafecafecafecafecafecafecafecafe-1234567890abcdef-01" {
		t.Fatalf("caller-set Traceparent must be preserved; got %q", got)
	}
}

func TestHTTPForward_CallerSetBaggage_Preserved(t *testing.T) {
	cap := &captureTransport{}
	client := &http.Client{Transport: core.HTTPForwardTransport(cap)}

	ctx := core.WithBaggage(context.Background(), "from=ctx")
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	req.Header.Set(core.HTTPHeaderBaggage, "from=caller")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := cap.lastReq.Header.Get(core.HTTPHeaderBaggage); got != "from=caller" {
		t.Fatalf("caller-set Baggage must be preserved; got %q", got)
	}
}

// --- request-isolation contract ---------------------------------------------

func TestHTTPForward_DoesNotMutateInputRequest(t *testing.T) {
	// http.RoundTripper contract: "RoundTrip should not modify the
	// request." Verify that even when we DO inject headers, the
	// caller's *http.Request is left untouched — we clone first.
	cap := &captureTransport{}
	transport := core.HTTPForwardTransport(cap)

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: validTraceparent,
		Tracestate:  "vendor=v",
	})
	original, _ := http.NewRequestWithContext(ctx, "GET", "http://example.invalid", nil)
	if _, err := transport.RoundTrip(original); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	if got := original.Header.Get(core.HTTPHeaderTraceparent); got != "" {
		t.Fatalf("original request must NOT be mutated; Traceparent leaked: %q", got)
	}
	if got := original.Header.Get(core.HTTPHeaderTracestate); got != "" {
		t.Fatalf("original request must NOT be mutated; Tracestate leaked: %q", got)
	}
	// And the clone the underlying transport saw must HAVE the headers.
	if got := cap.lastReq.Header.Get(core.HTTPHeaderTraceparent); got != validTraceparent {
		t.Fatalf("downstream request must carry the injected header; got %q", got)
	}
	if cap.lastReq == original {
		t.Fatalf("downstream transport must receive a CLONE, not the original request")
	}
}

// --- defaulting ------------------------------------------------------------

func TestHTTPForward_NilBase_FallsBackToDefaultTransport(t *testing.T) {
	// Caller passes nil → HTTPForwardTransport must substitute
	// http.DefaultTransport so the helper is usable in one line
	// (`Transport: core.HTTPForwardTransport(nil)`).
	transport := core.HTTPForwardTransport(nil)
	if transport == nil {
		t.Fatalf("HTTPForwardTransport(nil) returned nil; expected wrapped DefaultTransport")
	}

	// Spin up a real server and verify the wrapped transport can
	// actually round-trip a request. Using DefaultTransport (rather
	// than a fake) ensures the substitution works end-to-end.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: transport}
	ctx := core.WithBaggage(context.Background(), "userId=alice")
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do via default transport: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

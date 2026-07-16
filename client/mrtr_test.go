package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TestCallToolWithInputs_Elicitation drives a minimal SEP-2322 round-trip
// through the public client API: server returns InputRequiredResult asking
// for an elicitation, the DefaultInputHandler routes it to the client's
// registered elicitationHandler, the client retries with inputResponses,
// and the server's second invocation returns a complete ToolResult.
func TestCallToolWithInputs_Elicitation(t *testing.T) {
	srv := mrtrTestServer(t)

	c, _ := connectMRTRClient(t, srv,
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{
				Action: "accept",
				Content: map[string]any{
					"name": "Alice",
				},
			}, nil
		}),
	)

	res, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		client.DefaultInputHandler(c),
	)
	if err != nil {
		t.Fatalf("CallToolWithInputs: %v", err)
	}
	if res.IsInputRequired() || res.IsTask() {
		t.Fatalf("expected sync result, got %+v", res)
	}
	if res.Sync == nil || len(res.Sync.Content) == 0 {
		t.Fatalf("missing content; result=%+v", res)
	}
	text := res.Sync.Content[0].Text
	if !strings.Contains(text, "Alice") {
		t.Errorf("text=%q, want greeting with Alice", text)
	}
}

// TestCallToolWithInputs_MaxRounds verifies the loop bails out cleanly
// when the server keeps responding with InputRequiredResult past the
// configured cap. Without this guard, a misbehaving server could pin
// the client in an infinite retry loop.
func TestCallToolWithInputs_MaxRounds(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-cap", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "always_incomplete",
			Description: "Always asks for more input",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return ctx.RequestInput(core.InputRequests{
				"loop": core.InputRequest{Method: "elicitation/create", Params: json.RawMessage(`{}`)},
			})
		},
	)

	c, _ := connectMRTRClient(t, srv,
		client.WithElicitationHandler(func(context.Context, core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{}}, nil
		}),
	)

	_, err := client.CallToolWithInputs(context.Background(), c,
		"always_incomplete", map[string]any{},
		client.DefaultInputHandler(c),
		client.WithMaxMRTRRounds(3),
	)
	if !errors.Is(err, client.ErrMRTRMaxRounds) {
		t.Fatalf("err = %v, want ErrMRTRMaxRounds", err)
	}
}

// TestCallToolWithInputs_HandlerError verifies that an InputHandler error
// surfaces through CallToolWithInputs without retrying. (e.g. user
// declined elicitation, sampling provider unavailable.)
func TestCallToolWithInputs_HandlerError(t *testing.T) {
	srv := mrtrTestServer(t)
	c, _ := connectMRTRClient(t, srv)

	wantErr := errors.New("user declined")
	_, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
			return nil, wantErr
		},
	)
	if err == nil || !strings.Contains(err.Error(), "user declined") {
		t.Errorf("err = %v, want propagated handler error", err)
	}
}

// TestParseToolCallResult_InputRequired verifies the wire-shape probe
// recognises the SEP-2322 resultType:"input_required" discriminator and
// surfaces the InputRequiredResult payload to callers using the bare
// ToolCall API.
func TestParseToolCallResult_InputRequired(t *testing.T) {
	srv := mrtrTestServer(t)
	c, _ := connectMRTRClient(t, srv)

	// Bare ToolCall — should surface InputRequired on round 1.
	res, err := client.ToolCall(c, "test_tool_with_elicitation", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if !res.IsInputRequired() {
		t.Fatalf("expected InputRequired; got %+v", res)
	}
	if res.InputRequired.InputRequests["user_name"].Method != "elicitation/create" {
		t.Errorf("unexpected method: %+v", res.InputRequired.InputRequests)
	}
	if res.InputRequired.RequestState == "" {
		t.Error("expected non-empty requestState on InputRequired")
	}
}

// --- helpers ---

func mrtrTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-client-test", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_elicitation",
			Description: "Asks for user name then greets them",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)
	return srv
}

func connectMRTRClient(t *testing.T, srv *server.Server, opts ...client.ClientOption) (*client.Client, *httptest.Server) {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mrtr-client-test", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, ts
}

// --- SEP-414 P6: MRTR tracelink stamping (issue 682) ----------------------

const (
	mrtrTestTraceparent1 = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	mrtrTestTraceparent2 = "00-aabbccddeeff00112233445566778899-1122334455667788-01"
	mrtrTestTraceparent3 = "00-deadbeefcafebabe0123456789abcdef-fedcba9876543210-01"
)

// sequencedFakeTP returns a distinct TraceContext per StartSpan call so
// tests can verify the star-link semantic (round-2/3+ link to round 1,
// not the immediately-previous round). Indexed by call order.
type sequencedFakeTP struct {
	mu  sync.Mutex
	tcs []core.TraceContext
	idx int
}

func (p *sequencedFakeTP) StartSpan(ctx context.Context, _ string, _ ...core.Attribute) (context.Context, core.Span) {
	p.mu.Lock()
	var tc core.TraceContext
	if p.idx < len(p.tcs) {
		tc = p.tcs[p.idx]
	}
	p.idx++
	p.mu.Unlock()
	if !tc.IsZero() {
		ctx = core.WithTraceContext(ctx, tc)
	}
	return ctx, mrtrNoopSpan{}
}

type mrtrNoopSpan struct{}

func (mrtrNoopSpan) End()                     {}
func (mrtrNoopSpan) SetAttribute(_, _ string) {}
func (mrtrNoopSpan) RecordError(_ error)      {}
func (mrtrNoopSpan) AddLink(_ core.Link)      {}

// mrtrParamsCaptureMiddleware records the raw req.Params per round so
// tests can assert what `_meta.io.modelcontextprotocol/tracelink` the
// client actually stamped on the wire. Records every tools/call request
// in arrival order.
type mrtrParamsCaptureMiddleware struct {
	mu      sync.Mutex
	rounds  []json.RawMessage
}

func (m *mrtrParamsCaptureMiddleware) middleware() server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		if req.Method == "tools/call" {
			m.mu.Lock()
			// Copy so any later mutation doesn't bleed into our snapshot.
			cp := make(json.RawMessage, req.Params.Len())
			copy(cp, req.Params.Raw())
			m.rounds = append(m.rounds, cp)
			m.mu.Unlock()
		}
		return next(ctx, req)
	}
}

func (m *mrtrParamsCaptureMiddleware) snapshot() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]json.RawMessage, len(m.rounds))
	copy(out, m.rounds)
	return out
}

// extractTraceLink pulls out _meta.io.modelcontextprotocol/tracelink from
// a captured params payload for assertion. "" when absent.
func extractTraceLink(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var envelope struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal params: %v (raw=%s)", err, raw)
	}
	if envelope.Meta == nil {
		return ""
	}
	tp, _ := envelope.Meta[core.MetaKeyTraceLink].(string)
	return tp
}

// TestCallToolWithInputs_StampsTracelinkOnRound2 — minimal acceptance
// for issue 682. Round 1's outbound traceparent must appear as
// `_meta.io.modelcontextprotocol/tracelink` on round 2's outbound
// params (and NOT on round 1's — only rounds 2+ carry the link).
func TestCallToolWithInputs_StampsTracelinkOnRound2(t *testing.T) {
	capture := &mrtrParamsCaptureMiddleware{}
	srv := mrtrTestServerWithMiddleware(t, capture.middleware())

	tp := &sequencedFakeTP{tcs: []core.TraceContext{
		{Traceparent: mrtrTestTraceparent1},
		{Traceparent: mrtrTestTraceparent2},
	}}
	c, _ := connectMRTRClient(t, srv,
		client.WithTracerProvider(tp),
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{
				Action:  "accept",
				Content: map[string]any{"name": "Alice"},
			}, nil
		}),
	)

	_, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		client.DefaultInputHandler(c),
	)
	if err != nil {
		t.Fatalf("CallToolWithInputs: %v", err)
	}

	rounds := capture.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("got %d rounds, want 2", len(rounds))
	}
	if got := extractTraceLink(t, rounds[0]); got != "" {
		t.Errorf("round 1 MUST NOT carry tracelink (only rounds 2+ do); got %q", got)
	}
	if got := extractTraceLink(t, rounds[1]); got != mrtrTestTraceparent1 {
		t.Errorf("round 2 tracelink = %q, want round-1 traceparent %q", got, mrtrTestTraceparent1)
	}
}

// TestCallToolWithInputs_StarSemantic — round 3's tracelink MUST point
// at round 1, NOT round 2. The mcpkit decision is star (anchor =
// round 1), not chain (anchor = previous round). Backends following
// the link from round 3 land directly on the originating request.
func TestCallToolWithInputs_StarSemantic(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-star", Version: "0.0.1"})
	var calls int
	srv.RegisterTool(
		core.ToolDef{
			Name:        "three_round_tool",
			Description: "asks for input twice, then completes",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			calls++
			switch calls {
			case 1:
				return ctx.RequestInput(core.InputRequests{
					"q1": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Q1","requestedSchema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}`),
					},
				})
			case 2:
				return ctx.RequestInput(core.InputRequests{
					"q2": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Q2","requestedSchema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}`),
					},
				})
			default:
				return core.TextResult("done"), nil
			}
		},
	)
	capture := &mrtrParamsCaptureMiddleware{}
	srv = configureMiddleware(srv, capture.middleware())

	tp := &sequencedFakeTP{tcs: []core.TraceContext{
		{Traceparent: mrtrTestTraceparent1},
		{Traceparent: mrtrTestTraceparent2},
		{Traceparent: mrtrTestTraceparent3},
	}}
	c, _ := connectMRTRClient(t, srv,
		client.WithTracerProvider(tp),
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{"a": "ok"}}, nil
		}),
	)

	_, err := client.CallToolWithInputs(context.Background(), c,
		"three_round_tool", map[string]any{},
		client.DefaultInputHandler(c),
	)
	if err != nil {
		t.Fatalf("CallToolWithInputs: %v", err)
	}

	rounds := capture.snapshot()
	if len(rounds) != 3 {
		t.Fatalf("got %d rounds, want 3", len(rounds))
	}
	if got := extractTraceLink(t, rounds[1]); got != mrtrTestTraceparent1 {
		t.Errorf("round 2 tracelink = %q, want round 1 traceparent %q", got, mrtrTestTraceparent1)
	}
	if got := extractTraceLink(t, rounds[2]); got != mrtrTestTraceparent1 {
		t.Errorf("round 3 tracelink = %q, want round 1 traceparent %q (STAR semantic — not chain)", got, mrtrTestTraceparent1)
	}
}

// TestCallToolWithInputs_NoTracerProvider_NoTracelink pins zero
// overhead: without WithTracerProvider, the captured traceparent stays
// zero, and the InjectTraceLinkIntoParams call short-circuits, so the
// wire stays clean.
func TestCallToolWithInputs_NoTracerProvider_NoTracelink(t *testing.T) {
	capture := &mrtrParamsCaptureMiddleware{}
	srv := mrtrTestServerWithMiddleware(t, capture.middleware())

	c, _ := connectMRTRClient(t, srv,
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "Alice"}}, nil
		}),
	)

	_, err := client.CallToolWithInputs(context.Background(), c,
		"test_tool_with_elicitation", map[string]any{},
		client.DefaultInputHandler(c),
	)
	if err != nil {
		t.Fatalf("CallToolWithInputs: %v", err)
	}

	rounds := capture.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("got %d rounds, want 2", len(rounds))
	}
	for i, r := range rounds {
		if got := extractTraceLink(t, r); got != "" {
			t.Errorf("round %d carried tracelink %q despite no TracerProvider", i+1, got)
		}
	}
}

// mrtrTestServerWithMiddleware builds the same server as mrtrTestServer
// but registers the supplied middleware (used to capture inbound params
// per round for tracelink assertions).
func mrtrTestServerWithMiddleware(t *testing.T, mw server.Middleware) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-client-test", Version: "0.0.1"},
		server.WithMiddleware(mw),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_elicitation",
			Description: "Asks for user name then greets them",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)
	return srv
}

// configureMiddleware is a hack: server.WithMiddleware is a constructor
// option, so we use it via re-construction. For tests that register a
// tool BEFORE wanting the middleware, this works around that.
// In real adopters this would be a single NewServer call.
func configureMiddleware(srv *server.Server, mw server.Middleware) *server.Server {
	// In the three_round_tool test, we register the tool then need
	// middleware. The cleanest thing is to use server.UseMiddleware
	// post-construction. Verify that API exists.
	srv.UseMiddleware(mw)
	return srv
}

// TestDefaultInputHandler_DeterministicOrder pins sorted-key dispatch: the
// SEP-2322 wire is an unordered map, so without an explicit sort a
// multi-entry round presents in Go map-iteration order (different every run).
func TestDefaultInputHandler_DeterministicOrder(t *testing.T) {
	var presented []string
	c, _ := connectMRTRClient(t, mrtrTestServer(t),
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			presented = append(presented, req.Message)
			return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "x"}}, nil
		}),
	)

	reqs := core.InputRequests{}
	keys := []string{"h", "c", "f", "a", "e", "b", "g", "d"}
	for _, k := range keys {
		reqs[k] = core.InputRequest{
			Method: "elicitation/create",
			Params: json.RawMessage(`{"message":"` + k + `"}`),
		}
	}

	if _, err := client.DefaultInputHandler(c)(context.Background(), reqs); err != nil {
		t.Fatal(err)
	}
	want := "[a b c d e f g h]"
	if got := fmt.Sprint(presented); got != want {
		t.Fatalf("dispatch order = %v, want %v", got, want)
	}
}

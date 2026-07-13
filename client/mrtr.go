package client

// SEP-2322 MRTR (Multi Round-Trip Requests) — client side.
//
// On a tools/call response with resultType: "input_required", the client
// must resolve each entry in the returned inputRequests map into a response
// payload and retry the call with inputResponses + the echoed requestState.
// CallToolWithInputs runs that loop automatically; DefaultInputHandler
// bridges the most common method types (elicitation/create,
// sampling/createMessage, roots/list) onto the client's existing
// capability handlers (samplingHandler, elicitationHandler, rootsHandler).
// "input_required" was renamed from "incomplete" in SEP-2322 commit
// de6d76fb (merged 2026-05-06).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// InputHandler resolves an MRTR InputRequiredResult's inputRequests into the
// echoed inputResponses payload. Called once per retry round; returning
// an error aborts the loop.
//
// The map shape mirrors the wire contract: keys are server-chosen
// identifiers that MUST round-trip verbatim; values are opaque JSON
// payloads matching the InputRequest.Method (ElicitResult,
// CreateMessageResult, ListRootsResult, etc.).
type InputHandler func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error)

// MRTROption tunes CallToolWithInputs behavior.
type MRTROption func(*mrtrConfig)

type mrtrConfig struct {
	maxRounds int
}

// WithMaxMRTRRounds caps how many times CallToolWithInputs will retry a
// tools/call when the server keeps returning InputRequiredResult. Default is
// 16 (enough for any sane workflow; high enough that hitting it suggests
// a bug). Zero or negative values fall back to the default.
func WithMaxMRTRRounds(n int) MRTROption {
	return func(c *mrtrConfig) {
		if n > 0 {
			c.maxRounds = n
		}
	}
}

// ErrMRTRMaxRounds is returned by CallToolWithInputs when the server keeps
// asking for more input past the configured round cap. Wrap-able via
// errors.Is.
var ErrMRTRMaxRounds = errors.New("MRTR max rounds exceeded")

// CallToolWithInputs invokes a tool with automatic SEP-2322 MRTR retry.
// On InputRequiredResult, the handler is called to resolve the inputRequests;
// the call is then retried with inputResponses + the echoed requestState.
// The loop terminates as soon as the server returns a complete ToolResult,
// a CreateTaskResult, or an error. Returns ErrMRTRMaxRounds if the round
// cap (default 16) is hit.
//
// Pass DefaultInputHandler(c) to handle the standard inputRequest methods
// using the client's existing capability handlers.
func CallToolWithInputs(ctx context.Context, c *Client, name string, args any, handler InputHandler, opts ...MRTROption) (*ToolCallResult, error) {
	if handler == nil {
		return nil, errors.New("CallToolWithInputs: handler is required")
	}
	cfg := mrtrConfig{maxRounds: 16}
	for _, opt := range opts {
		opt(&cfg)
	}

	// SEP-414 P6 (issue 682): capture round 1's outbound traceparent so
	// every subsequent round can stamp it as
	// `_meta.io.modelcontextprotocol/tracelink`. Server-side trace
	// middleware reads the link and AddLink's the round-2+ dispatch span
	// back to round-1's, stitching the logical operation across separate
	// W3C traces. Star semantic — every round 2+ links to round 1, not
	// the immediately-previous round, so backends show round 1 as the
	// anchor regardless of how deep the loop goes.
	//
	// Holder remains zero when tracing isn't configured (Noop path) — the
	// per-round Inject is a no-op in that case, so this costs at most
	// one ctx.Value allocation on the unconfigured path.
	ctx, capturedTC := core.WithCapturedTraceContext(ctx)

	// First call: bare tools/call with no inputResponses. Use callImpl
	// so the captured-trace-context ctx (above) actually reaches the
	// trace middleware — Client.Call uses context.Background() and
	// would drop our holder.
	resp, err := c.callImpl(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	res, err := parseToolCallResult(resp.Raw)
	if err != nil {
		return nil, err
	}
	// Snapshot the round-1 identity (if any) before the holder gets
	// overwritten by round-2's middleware. Subsequent rounds all link
	// to this anchor, not to the immediately-previous round.
	round1TC := *capturedTC

	for round := 0; res.IsInputRequired(); round++ {
		if round >= cfg.maxRounds {
			return nil, fmt.Errorf("%w (rounds=%d, last requestState=%q)",
				ErrMRTRMaxRounds, cfg.maxRounds, res.InputRequired.RequestState)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		responses, err := handler(ctx, res.InputRequired.InputRequests)
		if err != nil {
			return nil, fmt.Errorf("MRTR input handler: %w", err)
		}

		// Retry the same tools/call with inputResponses + the echoed
		// requestState. requestState is omitted via omitempty when blank.
		params := map[string]any{
			"name":           name,
			"arguments":      args,
			"inputResponses": responses,
		}
		if res.InputRequired.RequestState != "" {
			params["requestState"] = res.InputRequired.RequestState
		}
		// Stamp the round-1 tracelink onto round-2+ params so the
		// server dispatch span AddLinks back to round 1. Zero-TC is a
		// no-op so the unconfigured path stays clean.
		var callParams any = params
		if !round1TC.IsZero() {
			callParams = core.InjectTraceLinkIntoParams(params, round1TC)
		}
		resp, err := c.callImpl(ctx, "tools/call", callParams)
		if err != nil {
			return nil, err
		}
		res, err = parseToolCallResult(resp.Raw)
		if err != nil {
			return nil, err
		}
	}

	return res, nil
}

// DefaultInputHandler returns an InputHandler that resolves the standard
// MRTR inputRequest methods using the client's existing capability
// handlers:
//
//   - "elicitation/create"     → samplingElicit via elicitationHandler
//   - "sampling/createMessage" → samplingHandler
//   - "roots/list"             → rootsHandler
//
// Unknown methods produce an error. Returns an error from the underlying
// handler too — CallToolWithInputs propagates it and aborts the loop.
//
// This is a starting point. Wrap or replace it for custom inputRequest
// methods, alternative routing, or to inject non-default response payloads
// (e.g., declining elicitation requests, returning canned sampling output
// in tests).
func DefaultInputHandler(c *Client) InputHandler {
	return func(ctx context.Context, reqs core.InputRequests) (core.InputResponses, error) {
		out := make(core.InputResponses, len(reqs))
		for key, req := range reqs {
			payload, err := dispatchMRTRInputRequest(ctx, c, req)
			if err != nil {
				return nil, fmt.Errorf("inputRequest %q (%s): %w", key, req.Method, err)
			}
			out[key] = payload
		}
		return out, nil
	}
}

// dispatchMRTRInputRequest synthesizes a server-to-client request from an
// MRTR InputRequest and routes it through the same dispatcher the
// transport uses for real server-initiated requests
// (Client.HandleServerRequestWithContext). Result: a single source of
// truth for "client received MCP method X, here's the response", URL-mode
// elicitation gating for free, and any future client middleware applies
// uniformly to both real server requests and MRTR-synthesized ones.
//
// The synthetic request ID is irrelevant — the response never goes back
// over the wire, only its result/error is consumed.
func dispatchMRTRInputRequest(ctx context.Context, c *Client, req core.InputRequest) (json.RawMessage, error) {
	synth := &core.Request{
		Method: req.Method,
		Params: core.NewRawJSON(req.Params),
		ID:     json.RawMessage(`"mrtr"`),
	}
	resp := c.HandleServerRequestWithContext(ctx, synth)
	if resp == nil {
		return nil, fmt.Errorf("MRTR dispatch %q returned nil response", req.Method)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %s (code=%d)", req.Method, resp.Error.Message, resp.Error.Code)
	}
	return json.Marshal(resp.Result)
}

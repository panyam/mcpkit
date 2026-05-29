package main

// SEP-2322 input-required-result fixtures.
//
// Implement the tool / prompt contracts the upstream conformance scenarios
// `input-required-result-*` exercise. Each handler branches on
// ctx.HasInputResponses(): the first call returns ctx.RequestInput(...) and
// the second call decodes the echoed response into a final result. The
// dispatch layer reshapes the InputRequiredResult onto the wire and mints
// the requestState; handlers never touch the token directly.
//
// Tool naming mirrors the upstream `input-required-result-*` scenarios
// verbatim — the existing examples/mrtr/ fixture (which targets an older
// SEP-fork branch under `test_incomplete_result_*` names) stays as-is.

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func registerInputRequiredResultFixtures(srv *server.Server) {
	registerInputRequiredResultTools(srv)
	registerInputRequiredResultPrompts(srv)
}

func registerInputRequiredResultTools(srv *server.Server) {
	// A1: basic elicitation — one round-trip asking for the user's name.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_elicitation",
		"Asks for the user's name then greets them (SEP-2322 A1, A7, A10, A14, A15).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "What is your name?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
					}),
				})
			}
			raw := ctx.InputResponse("user_name")
			if raw == nil {
				// Spec recommendation: re-request rather than error when the
				// echoed inputResponses miss the expected key. A7 grades this
				// as a soft preference (WARNING, not FAILURE).
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "I need your name to greet you.",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
					}),
				})
			}
			er, err := core.DecodeElicitationInputResponse(raw)
			if err != nil {
				return core.ErrorResult("malformed elicitation response: " + err.Error()), nil
			}
			name, _ := er.Content["name"].(string)
			if name == "" {
				return core.ErrorResult("elicitation accepted but name was empty"), nil
			}
			return core.TextResult(fmt.Sprintf("Hello, %s!", name)), nil
		},
	))

	// A2: basic sampling — one round-trip asking the client's LLM for the
	// capital of France.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_sampling",
		"Asks the client to sample its LLM for the capital of France (SEP-2322 A2).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"capital_question": core.NewSamplingInputRequest(core.CreateMessageRequest{
						Messages: []core.SamplingMessage{{
							Role:    "user",
							Content: core.Content{Type: "text", Text: "What is the capital of France?"},
						}},
						MaxTokens: 100,
					}),
				})
			}
			sr, err := core.DecodeSamplingInputResponse(ctx.InputResponse("capital_question"))
			if err != nil {
				return core.ErrorResult("malformed sampling response: " + err.Error()), nil
			}
			return core.TextResult("model said: " + sr.Content.Text), nil
		},
	))

	// A3: basic roots/list — one round-trip asking the client for its
	// current workspace roots.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_list_roots",
		"Asks the client for its current workspace roots (SEP-2322 A3).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"client_roots": core.NewListRootsInputRequest(),
				})
			}
			roots, err := core.DecodeListRootsInputResponse(ctx.InputResponse("client_roots"))
			if err != nil {
				return core.ErrorResult("malformed roots/list response: " + err.Error()), nil
			}
			uris := make([]string, 0, len(roots.Roots))
			for _, r := range roots.Roots {
				uris = append(uris, r.URI)
			}
			return core.TextResult(fmt.Sprintf("client roots: %v", uris)), nil
		},
	))

	// A4: requestState round-trip — the response text on round 2 MUST
	// include "state-ok" to confirm the server received + validated the
	// echoed requestState. Token integrity is the dispatch layer's job;
	// the handler just observes ctx.RequestState() is non-empty.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_request_state",
		"Asks for a confirmation, then confirms requestState was echoed verbatim (SEP-2322 A4).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"confirm": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "Please confirm",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
					}),
				})
			}
			if ctx.RequestState() == "" {
				return core.ErrorResult("requestState was not echoed back"), nil
			}
			return core.TextResult("state-ok: requestState round-tripped through the client"), nil
		},
	))

	// A5: multiple input requests — elicitation + sampling + roots/list
	// asked in a single round.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_multiple_inputs",
		"Asks for elicitation + sampling + roots/list in one round (SEP-2322 A5).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "What is your name?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
					}),
					"greeting": core.NewSamplingInputRequest(core.CreateMessageRequest{
						Messages: []core.SamplingMessage{{
							Role:    "user",
							Content: core.Content{Type: "text", Text: "Generate a greeting"},
						}},
						MaxTokens: 50,
					}),
					"client_roots": core.NewListRootsInputRequest(),
				})
			}
			return core.TextResult("all inputs received"), nil
		},
	))

	// A6: multi-round — three rounds with rotating requestState. Each
	// round mints a fresh token via mintRequestState (dispatch handles
	// it transparently).
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_multi_round",
		"Two elicitation rounds (step1 → step2) then a complete result (SEP-2322 A6).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			step1 := ctx.InputResponse("step1")
			step2 := ctx.InputResponse("step2")
			switch {
			case step1 == nil:
				return ctx.RequestInput(core.InputRequests{
					"step1": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "Step 1: What is your name?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
					}),
				})
			case step2 == nil:
				return ctx.RequestInput(core.InputRequests{
					"step2": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "Step 2: What is your favorite color?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"color":{"type":"string"}},"required":["color"]}`),
					}),
				})
			default:
				return core.TextResult("multi-round complete"), nil
			}
		},
	))

	// A12: tampered-state — relies on the dispatch layer signing the
	// requestState (server.WithRequestStateSigning enabled in main.go).
	// Handler shape is identical to A1; integrity-verification happens
	// upstream before the second-round handler even runs.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_tampered_state",
		"Same shape as A1 — exercises signed-requestState rejection (SEP-2322 A12).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"confirm": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "Please confirm",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
					}),
				})
			}
			return core.TextResult("tamper test ok"), nil
		},
	))

	// A13: capability check — the client declares `sampling` only
	// (no `elicitation`). The handler MUST skip the elicitation
	// inputRequest and emit sampling-only.
	srv.Register(core.TypedTool[emptyInput, core.ToolResponse](
		"test_input_required_result_capabilities",
		"Gates inputRequests on per-request client capabilities (SEP-2322 A13).",
		func(ctx core.ToolContext, _ emptyInput) (core.ToolResponse, error) {
			if ctx.HasInputResponses() {
				return core.TextResult("capability-check complete"), nil
			}
			reqs := core.InputRequests{}
			caps := ctx.ClientCaps()
			if caps != nil && caps.Sampling != nil {
				reqs["greeting"] = core.NewSamplingInputRequest(core.CreateMessageRequest{
					Messages: []core.SamplingMessage{{
						Role:    "user",
						Content: core.Content{Type: "text", Text: "Generate a greeting"},
					}},
					MaxTokens: 50,
				})
			}
			if caps != nil && caps.Elicitation != nil {
				reqs["user_name"] = core.NewElicitationInputRequest(core.ElicitationRequest{
					Message:         "What is your name?",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
				})
			}
			return ctx.RequestInput(reqs)
		},
	))
}

func registerInputRequiredResultPrompts(srv *server.Server) {
	// A9: prompts/get returning InputRequiredResult — verifies SEP-2322
	// is universal across request methods, not tools-only.
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "test_input_required_result_prompt",
			Description: "Prompts the client for a context value, then builds a templated prompt (SEP-2322 A9).",
		},
		func(ctx core.PromptContext, _ core.PromptRequest) (core.PromptResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_context": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "What context should the prompt use?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"context":{"type":"string"}},"required":["context"]}`),
					}),
				})
			}
			er, err := core.DecodeElicitationInputResponse(ctx.InputResponse("user_context"))
			if err != nil {
				return core.PromptResult{}, fmt.Errorf("decode user_context: %w", err)
			}
			ctxStr, _ := er.Content["context"].(string)
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Context: " + ctxStr},
				}},
			}, nil
		},
	)
}

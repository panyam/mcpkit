// Example: SEP-2322 MRTR (Multi Round-Trip Requests) — ephemeral
// InputRequiredResult flow.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080
//	Terminal 2:  make demo          # demokit walkthrough (or `make demo --tui`)
//
// The server is a real MCP server — any host can connect to it. The
// walkthrough acts as a scripted MCP host that drives `tools/call`
// through one full InputRequiredResult round-trip and prints the wire
// fields at each step.
//
// The same binary doubles as the conformance fixture: pass `--serve`
// to run the server. The conformance/mrtr/ suite spawns one process and
// drives the seven scenarios against it.
//
// Tools (named to match the upstream conformance contract —
// modelcontextprotocol/conformance PR 188):
//
//   - test_tool_with_elicitation             (A1) basic elicitation round-trip
//   - test_incomplete_result_sampling        (A2) basic sampling round-trip
//   - test_incomplete_result_list_roots      (A3) basic roots/list round-trip
//   - test_incomplete_result_request_state   (A4) requestState echo
//   - test_incomplete_result_multiple_inputs (A5) multiple inputRequests in one round
//   - test_incomplete_result_multi_round     (A6) two rounds of elicitation
//   - test_incomplete_result_elicitation     (A7) graceful handling when client
//                                                 sends a wrong inputResponses key
//
// The MRTR loop is server-driven and stateless: the server returns
// InputRequiredResult{inputRequests, requestState}; the client retries the
// SAME tools/call with inputResponses + the echoed requestState. Dispatch
// transparently merges accumulated answers across rounds via requestState
// (see core.MRTRRoundState), so handlers stay stateless.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	signingKey := flag.String("signing-key", "mrtr-demo-signing-key", "HMAC key for requestState signing (empty = plaintext mode)")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("mrtr-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	var extraOpts []server.Option
	if *signingKey != "" {
		extraOpts = append(extraOpts, server.WithRequestStateSigning([]byte(*signingKey), 24*time.Hour))
	}

	if err := common.RunServer(common.ServerConfig{
		Name:           "mrtr-demo",
		Addr:           *addr,
		TracerProvider: tp,
		Options:        extraOpts,
		Register: func(srv *server.Server) {
			registerMRTRTools(srv)
			// SEP-2322 + SEP-2663 composition: the registry holds at least one
			// task-eligible tool (test_tool_with_task) that gathers input via
			// MRTR rounds and then escalates to async via the GoAsync sentinel.
			// The taskV2Middleware is what observes the GoAsync return and spawns
			// the continuation goroutine.
			tasks.Register(tasks.Config{Server: srv})
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// --- Tool registrations ---

func registerMRTRTools(srv *server.Server) {
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_elicitation",
			Description: "A1: round 1 asks for user_name via elicitation; round 2 returns greeting.",
			InputSchema: map[string]any{"type": "object"},
		},
		basicElicitationTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_sampling",
			Description: "A2: round 1 asks for capital_question via sampling; round 2 echoes the answer back as text.",
			InputSchema: map[string]any{"type": "object"},
		},
		basicSamplingTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_list_roots",
			Description: "A3: round 1 asks for client_roots via roots/list; round 2 returns the roots.",
			InputSchema: map[string]any{"type": "object"},
		},
		basicListRootsTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_request_state",
			Description: "A4: round 1 asks for confirm + emits requestState; round 2 validates the echoed state.",
			InputSchema: map[string]any{"type": "object"},
		},
		requestStateTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_multiple_inputs",
			Description: "A5: round 1 asks for elicitation + sampling + roots in one InputRequiredResult.",
			InputSchema: map[string]any{"type": "object"},
		},
		multipleInputsTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_multi_round",
			Description: "A6: two rounds of elicitation (step1 then step2), then a complete result.",
			InputSchema: map[string]any{"type": "object"},
		},
		multiRoundTool,
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_elicitation",
			Description: "A7: same as A1 but tolerant of wrong inputResponses keys — re-requests instead of erroring.",
			InputSchema: map[string]any{"type": "object"},
		},
		basicElicitationTool, // same handler — already re-requests on missing key
	)

	// A8: SEP-2322 + SEP-2663 composition. The handler runs through an MRTR
	// round (gathering user_name) and only then escalates to async by
	// returning the GoAsync sentinel. taskV2Middleware observes the GoAsync
	// return and spawns a continuation goroutine that re-invokes the handler
	// with a TaskContext attached; the goroutine does the "expensive work"
	// and stores the final ToolResult on the task. Conformance scenario
	// "mrtr-08" / mrtr-tasks-composition drives this fixture.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_task",
			Description: "A8: MRTR elicit for user_name, then escalate to async via GoAsync. The continuation goroutine does the work and returns a greeting.",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		mrtrTaskCompositionTool,
	)
}

// --- A1: basic elicitation ---

func basicElicitationTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resp := ctx.InputResponse("user_name")
	if resp == nil {
		return ctx.RequestInput(core.InputRequests{
			"user_name": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
			},
		})
	}
	var er struct {
		Action  string `json:"action"`
		Content struct {
			Name string `json:"name"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp, &er); err != nil {
		return core.ErrorResult("malformed elicitation response: " + err.Error()), nil
	}
	if er.Content.Name == "" {
		return core.ErrorResult("missing name in elicitation content"), nil
	}
	return core.TextResult(fmt.Sprintf("Hello, %s!", er.Content.Name)), nil
}

// --- A2: basic sampling ---

func basicSamplingTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resp := ctx.InputResponse("capital_question")
	if resp == nil {
		return ctx.RequestInput(core.InputRequests{
			"capital_question": core.InputRequest{
				Method: "sampling/createMessage",
				Params: json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"What is the capital of France?"}}],"maxTokens":100}`),
			},
		})
	}
	// CreateMessageResult shape: {role, content:{type, text}, model, stopReason}
	var sr struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp, &sr); err != nil {
		return core.ErrorResult("malformed sampling response: " + err.Error()), nil
	}
	return core.TextResult("model said: " + sr.Content.Text), nil
}

// --- A3: basic list roots ---

func basicListRootsTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resp := ctx.InputResponse("client_roots")
	if resp == nil {
		return ctx.RequestInput(core.InputRequests{
			"client_roots": core.InputRequest{
				Method: "roots/list",
				Params: json.RawMessage(`{}`),
			},
		})
	}
	var lr struct {
		Roots []struct {
			URI  string `json:"uri"`
			Name string `json:"name"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(resp, &lr); err != nil {
		return core.ErrorResult("malformed roots/list response: " + err.Error()), nil
	}
	parts := make([]string, 0, len(lr.Roots))
	for _, r := range lr.Roots {
		parts = append(parts, r.URI)
	}
	return core.TextResult("client roots: " + strings.Join(parts, ", ")), nil
}

// --- A4: requestState round-trip ---

func requestStateTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resp := ctx.InputResponse("confirm")
	if resp == nil {
		return ctx.RequestInput(core.InputRequests{
			"confirm": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"Please confirm","requestedSchema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}}`),
			},
		})
	}
	// Dispatch already validated requestState by the time we get here. The
	// scenario contract asks the response text to include "state-ok" so the
	// conformance test can confirm round-trip integrity.
	return core.TextResult("state-ok: requestState round-trip verified"), nil
}

// --- A5: multiple inputs in one round ---

func multipleInputsTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	if !ctx.HasInputResponses() {
		return ctx.RequestInput(core.InputRequests{
			"user_name": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
			},
			"greeting": core.InputRequest{
				Method: "sampling/createMessage",
				Params: json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"Generate a greeting"}}],"maxTokens":50}`),
			},
			"client_roots": core.InputRequest{
				Method: "roots/list",
				Params: json.RawMessage(`{}`),
			},
		})
	}
	// On the retry, all three keys must be present. Missing keys → re-request.
	missing := []string{}
	for _, k := range []string{"user_name", "greeting", "client_roots"} {
		if ctx.InputResponse(k) == nil {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return core.ErrorResult("missing inputResponses: " + strings.Join(missing, ", ")), nil
	}
	return core.TextResult("got name + greeting + roots"), nil
}

// --- A6: multi-round (step1 → step2 → complete) ---

func multiRoundTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	if ctx.InputResponse("step1") == nil {
		return ctx.RequestInput(core.InputRequests{
			"step1": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"Step 1: What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
			},
		})
	}
	if ctx.InputResponse("step2") == nil {
		return ctx.RequestInput(core.InputRequests{
			"step2": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"Step 2: What is your favorite color?","requestedSchema":{"type":"object","properties":{"color":{"type":"string"}},"required":["color"]}}`),
			},
		})
	}
	var s1 struct {
		Content struct {
			Name string `json:"name"`
		} `json:"content"`
	}
	var s2 struct {
		Content struct {
			Color string `json:"color"`
		} `json:"content"`
	}
	json.Unmarshal(ctx.InputResponse("step1"), &s1)
	json.Unmarshal(ctx.InputResponse("step2"), &s2)
	return core.TextResult(fmt.Sprintf("Hi %s, your favorite color is %s.", s1.Content.Name, s2.Content.Color)), nil
}

// --- A8: MRTR → Tasks composition (SEP-2322 + SEP-2663) ---

// mrtrTaskCompositionTool walks two distinct phases that meet at the GoAsync
// sentinel:
//
//   Phase 1 (synchronous, no TaskContext):
//     - If user_name hasn't been answered yet → return InputRequiredResult via
//       ctx.RequestInput. taskV2Middleware sees IncompleteResult and lets it
//       through; the client sees a normal MRTR InputRequiredResult.
//     - Once user_name is present → return ToolResult{GoAsync: true}.
//       taskV2Middleware mints a fresh task and spawns the continuation.
//
//   Phase 2 (in the continuation goroutine, TaskContext attached):
//     - Detect the TaskContext, do the "expensive" work, return a normal
//       ToolResult. taskV2Middleware stores it as the task's terminal result;
//       the client retrieves it via tasks/get.
//
// Per SEP-2663 spec separation rules:
//   - MRTR requestState is NOT carried into the task's requestState (the task
//     gets its own per-task inputState).
//   - Task inputRequests keys (if the goroutine called TaskElicit / TaskSample)
//     are scoped to the task's lifetime — distinct from the MRTR phase keys.
//   - Clients do NOT need to deduplicate across the two flows.
func mrtrTaskCompositionTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	// Phase 2: running inside the continuation goroutine. The TaskContext
	// gates us into the async branch.
	if tasks.GetTaskContext(ctx) != nil {
		// "Expensive" work simulator. A real handler would call out to a
		// downstream service, run a long computation, etc.
		time.Sleep(50 * time.Millisecond)
		var er struct {
			Action  string `json:"action"`
			Content struct {
				Name string `json:"name"`
			} `json:"content"`
		}
		if raw := ctx.InputResponse("user_name"); raw != nil {
			_ = json.Unmarshal(raw, &er)
		}
		if er.Content.Name == "" {
			return core.ErrorResult("task continuation lost user_name"), nil
		}
		return core.TextResult(fmt.Sprintf("Hello, %s! (computed in task)", er.Content.Name)), nil
	}

	// Phase 1: synchronous. Drive the MRTR round-trip for user_name first; do
	// NOT mint a task until the input is in hand.
	if ctx.InputResponse("user_name") == nil {
		return ctx.RequestInput(core.InputRequests{
			"user_name": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
			},
		})
	}

	// MRTR loop is complete; escalate to async so the heavy work happens
	// in the continuation goroutine (with the SEP-2663 G6 filter applied).
	return core.GoAsyncResult{}, nil
}

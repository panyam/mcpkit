// Example: SEP-2322 MRTR (Multi Round-Trip Requests) — ephemeral
// IncompleteResult flow.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080
//	Terminal 2:  make demo          # demokit walkthrough (or `make demo --tui`)
//
// The server is a real MCP server — any host can connect to it. The
// walkthrough acts as a scripted MCP host that drives `tools/call`
// through one full IncompleteResult round-trip and prints the wire
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
// IncompleteResult{inputRequests, requestState}; the client retries the
// SAME tools/call with inputResponses + the echoed requestState. Dispatch
// transparently merges accumulated answers across rounds via requestState
// (see core.MRTRRoundState), so handlers stay stateless.
package main

import (
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
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	logger := common.NewMCPLogger("[mcp] ")
	opts := []server.Option{server.WithListen(*addr)}
	opts = append(opts, common.WithMCPLogging(logger)...)
	if *signingKey != "" {
		opts = append(opts, server.WithRequestStateSigning([]byte(*signingKey), 24*time.Hour))
	}

	srv := server.NewServer(core.ServerInfo{Name: "mrtr-demo", Version: "0.1.0"}, opts...)

	registerMRTRTools(srv)

	log.Printf("[mrtr-demo] listening on %s", *addr)
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
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
			Description: "A5: round 1 asks for elicitation + sampling + roots in one IncompleteResult.",
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
}

// --- A1: basic elicitation ---

func basicElicitationTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

func basicSamplingTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

func basicListRootsTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

func requestStateTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

func multipleInputsTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

func multiRoundTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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

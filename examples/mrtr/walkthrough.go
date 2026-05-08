package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

func runDemo() {
	serverURL := common.ServerURL()

	demo := demokit.New("MCP MRTR (SEP-2322) — Ephemeral InputRequiredResult Round-Trips").
		Dir("mrtr").
		Description("Walks through the SEP-2322 ephemeral Multi Round-Trip Requests flow. The server returns `InputRequiredResult{inputRequests, requestState}` when it needs more input from the client; the client resolves each `inputRequest` (elicitation, sampling, roots) locally and retries the SAME `tools/call` with `inputResponses` + the echoed `requestState`. Stateless on the server side — accumulated answers live inside `requestState` across rounds. Renamed from `IncompleteResult` in SEP-2322 commit de6d76fb (merged 2026-05-06).").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # MRTR demo server on :8080",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
	)

	demo.Section("What MRTR adds to tools/call",
		"v1 `tools/call` had two terminal shapes — a sync `ToolResult` or (with SEP-2663 Tasks) a `CreateTaskResult`. SEP-2322 adds a third **transient** shape:",
		"",
		"- **`resultType: \"complete\"`** (or absent) — sync ToolResult, the call is done.",
		"- **`resultType: \"task\"`** — server elected to spin off a task; client polls via `tasks/get` (SEP-2663).",
		"- **`resultType: \"input_required\"`** — server needs more input. The response carries `inputRequests` (a map of opaque keys → `{method, params}`) and an opaque `requestState`. The client resolves each input request locally, then RETRIES the same `tools/call` with the original arguments PLUS `inputResponses` (keyed by the same opaque ids) AND the echoed `requestState`. Renamed from `\"incomplete\"` in SEP-2322 commit de6d76fb (merged 2026-05-06).",
		"",
		"The `inputRequests` methods are real MCP method names (`elicitation/create`, `sampling/createMessage`, `roots/list`). The client routes each through the same dispatcher it uses for real server-initiated requests — `client.HandleServerRequestWithContext` — so your existing `WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` callbacks just work.",
		"",
		"`client.CallToolWithInputs(ctx, c, name, args, handler)` runs the loop automatically; `client.DefaultInputHandler(c)` is the standard handler that delegates to the client's capability callbacks.",
	)

	var c *client.Client

	demo.Step("Connect to the MRTR server with capability handlers").
		Arrow("Host", "Server", "POST /mcp — initialize (capabilities: elicitation, sampling, roots)").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("`client.WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` register the client-side callbacks. The walkthrough returns canned answers so the loop runs end-to-end without user interaction; in production these would prompt the user, hit an LLM, or read filesystem roots.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "mrtr-demo-host", Version: "1.0"},
				client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
					// Canned answer — accepts and returns Alice for any name elicitation.
					return core.ElicitationResult{
						Action:  "accept",
						Content: map[string]any{"name": "Alice"},
					}, nil
				}),
				client.WithSamplingHandler(func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
					return core.CreateMessageResult{
						Role:       "assistant",
						Content:    core.Content{Type: "text", Text: "Paris"},
						Model:      "demo-stub",
						StopReason: "endTurn",
					}, nil
				}),
				client.WithRootsHandler(func(ctx context.Context) ([]core.Root, error) {
					return []core.Root{{URI: "file:///demo/root", Name: "Demo Root"}}, nil
				}),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	demo.Step("Round 1 (raw): tools/call → InputRequiredResult").
		Arrow("Host", "Server", "tools/call: test_tool_with_elicitation {}").
		DashedArrow("Server", "Host", "{ resultType: \"input_required\", inputRequests: {user_name: {method: \"elicitation/create\", ...}}, requestState: \"<token>\" }").
		Note("Bypass the auto-loop helper to see the raw InputRequiredResult shape. The discriminator is `resultType` — camelCase like every other MCP wire field. `inputRequests` is keyed by server-chosen opaque ids the client must echo verbatim. SEP-2322 commit de6d76fb (merged 2026-05-06) renamed this variant from IncompleteResult / `\"incomplete\"`.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "test_tool_with_elicitation", map[string]any{})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if !res.IsInputRequired() {
				fmt.Printf("    UNEXPECTED: round 1 was not InputRequired (got %+v)\n", res)
				return
			}
			pretty, _ := json.MarshalIndent(res.InputRequired, "    ", "  ")
			fmt.Printf("    raw InputRequiredResult:\n%s\n", string(pretty))
			return
		})

	demo.Step("Auto-loop: CallToolWithInputs runs the round-trip").
		Arrow("Host", "Server", "tools/call: test_tool_with_elicitation").
		DashedArrow("Server", "Host", "InputRequiredResult{user_name elicitation}").
		Arrow("Host", "Host", "DefaultInputHandler → c.elicitationHandler → ElicitationResult{name: Alice}").
		Arrow("Host", "Server", "tools/call (retry): {arguments: {}, inputResponses: {user_name: <result>}, requestState: <echo>}").
		DashedArrow("Server", "Host", "ToolResult: \"Hello, Alice!\"").
		Note("`client.CallToolWithInputs(ctx, c, name, args, handler)` collapses the whole loop. `DefaultInputHandler` synthesizes a server-to-client request for each `inputRequest` and routes it through `client.HandleServerRequestWithContext` — single source of truth for how the client responds to MCP method requests, whether they arrived over the back-channel or inlined inside an InputRequiredResult.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			bgCtx := context.Background()
			res, err := client.CallToolWithInputs(bgCtx, c,
				"test_tool_with_elicitation", map[string]any{},
				client.DefaultInputHandler(c),
			)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if res.IsInputRequired() || res.IsTask() {
				fmt.Printf("    UNEXPECTED: expected sync ToolResult, got %+v\n", res)
				return
			}
			fmt.Printf("    final result: %s\n", res.Sync.Content[0].Text)
			return
		})

	demo.Step("Multi-round: server accumulates answers across rounds via requestState").
		Arrow("Host", "Server", "tools/call: test_incomplete_result_multi_round").
		DashedArrow("Server", "Host", "Round 1 InputRequiredResult: ask step1 (name)").
		Arrow("Host", "Server", "retry with inputResponses{step1}").
		DashedArrow("Server", "Host", "Round 2 InputRequiredResult: ask step2 (color) — requestState now carries step1's answer").
		Arrow("Host", "Server", "retry with inputResponses{step2} (NOT step1 — that's already in requestState)").
		DashedArrow("Server", "Host", "Round 3 ToolResult: \"Hi Alice, your favorite color is Alice.\"").
		Note("The wire only ships the LATEST round's `inputResponses`. Dispatch decodes prior answers from `requestState` (a signed `MRTRRoundState` containing the accumulated answers map), merges with the current round, and surfaces a unified map to the handler. Handlers stay stateless across rounds. The canned elicitation handler returns the same `name: Alice` for both prompts in this demo, hence the funny output — a real handler would branch on the elicitation message.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			bgCtx := context.Background()
			res, err := client.CallToolWithInputs(bgCtx, c,
				"test_incomplete_result_multi_round", map[string]any{},
				client.DefaultInputHandler(c),
			)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if res.Sync == nil || len(res.Sync.Content) == 0 {
				fmt.Printf("    UNEXPECTED: no sync result content\n")
				return
			}
			fmt.Printf("    final result: %s\n", res.Sync.Content[0].Text)
			return
		})

	demo.Section("Where to look in the code",
		"- Server dispatch: `server/dispatch.go` (handleToolsCall reshapes InputRequired into the wire envelope; merges accumulated answers from `requestState`)",
		"- Server runtime: `server/mrtr.go` (`mrtrRuntime` — sign / verify / mint requestState tokens; `WithRequestStateSigning(key, ttl)` shared with SEP-2663 Tasks)",
		"- Wire types: `core.InputRequiredResult` / `MRTRRoundState` / `Sign|VerifyMRTRState` — core/task_v2.go",
		"- Tool handler API: `ctx.RequestInput(reqs)` sentinel + `ctx.InputResponse(key)` / `HasInputResponses()` / `RequestState()` accessors — core/handler_context.go",
		"- Client auto-loop: `client.CallToolWithInputs` + `DefaultInputHandler` — client/mrtr.go",
		"- Client dispatch unification: `client.HandleServerRequestWithContext` — single switch for both real server-initiated requests AND MRTR-synthesized ones — client/client.go",
		"- Conformance: panyam/mcpconformance fork (`src/scenarios/server/mrtr/`, 7 checks + 1 SKIPPED composition; upstream Draft PR modelcontextprotocol/conformance#262; `make testconf-mrtr` runs it)",
		"- SEP-2322 spec: https://github.com/modelcontextprotocol/specification/pull/2322",
	)

	common.SetupRenderer(demo)

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

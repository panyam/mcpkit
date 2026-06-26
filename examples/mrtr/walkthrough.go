package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func runDemo() {
	serverURL := common.ServerURL()
	wire := common.WireFromArgs()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("mrtr-demo-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

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
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Initialize a session declaring the capabilities the server's inputRequests
# will exercise (elicitation, sampling, roots), then capture the session id.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"elicitation":{},"sampling":{},"roots":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
echo "SID=$SID"`).Default(),
			demokit.MakeVariant("go", "go", `// Register the client-side capability handlers MRTR drives. The canned
// answers keep the demo non-interactive; in production they prompt the user,
// hit an LLM, or read filesystem roots.
c := client.NewClient(serverURL+"/mcp",
    core.ClientInfo{Name: "mrtr-demo-host", Version: "1.0"},
    client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
        return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "Alice"}}, nil
    }),
    client.WithSamplingHandler(func(ctx context.Context, req core.CreateMessageRequest) (core.CreateMessageResult, error) {
        return core.CreateMessageResult{Role: "assistant", Content: core.Content{Type: "text", Text: "Paris"}, Model: "demo-stub", StopReason: "endTurn"}, nil
    }),
    client.WithRootsHandler(func(ctx context.Context) ([]core.Root, error) {
        return []core.Root{{URI: "file:///demo/root", Name: "Demo Root"}}, nil
    }),
)
if err := c.Connect(); err != nil { /* server not up — run: make serve */ }`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			opts := []client.ClientOption{
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
				client.WithTracerProvider(tp),
			}
			if opt, ok := wire.ClientOption(); ok {
				opts = append(opts, opt)
			}
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "mrtr-demo-host", Version: "1.0"},
				opts...,
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
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# A bare tools/call. The server needs input, so result comes back with
# resultType:"input_required", an inputRequests map (opaque key -> {method,
# params}), and an opaque requestState you echo verbatim on retry.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool_with_elicitation","arguments":{}}}' \
  | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// client.ToolCall returns a polymorphic *ToolCallResult. IsInputRequired()
// is the third terminal shape (alongside sync + task).
res, _ := client.ToolCall(c, "test_tool_with_elicitation", map[string]any{})
if res.IsInputRequired() {
    // res.InputRequired.InputRequests — opaque key -> {method, params}
    // res.InputRequired.RequestState  — echo verbatim on the retry
}`),
		).
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
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Round 1: tools/call returns input_required + requestState.
R1=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool_with_elicitation","arguments":{}}}')
STATE=$(echo "$R1" | jq -r '.result.requestState')

# Resolve the elicitation locally (canned {name: Alice} here), then RETRY the
# same tools/call with inputResponses (keyed by the opaque id round 1 returned,
# "user_name") PLUS the echoed requestState.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"test_tool_with_elicitation\",\"arguments\":{},\"inputResponses\":{\"user_name\":{\"action\":\"accept\",\"content\":{\"name\":\"Alice\"}}},\"requestState\":\"$STATE\"}}" \
  | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// CallToolWithInputs runs the whole round-trip: it resolves each inputRequest
// via DefaultInputHandler (which routes to the WithXHandler callbacks) and
// retries with inputResponses + requestState until a terminal result.
res, _ := client.CallToolWithInputs(context.Background(), c,
    "test_tool_with_elicitation", map[string]any{},
    client.DefaultInputHandler(c),
)
// res.Sync.Content[0].Text == "Hello, Alice!"`),
		).
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
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Round 1: server asks step1 (name).
R1=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_incomplete_result_multi_round","arguments":{}}}')
S1=$(echo "$R1" | jq -r '.result.requestState')

# Round 2: retry with step1's answer. Server asks step2 (color); the new
# requestState now also encodes step1's answer.
R2=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"test_incomplete_result_multi_round\",\"arguments\":{},\"inputResponses\":{\"step1\":{\"action\":\"accept\",\"content\":{\"name\":\"Alice\"}}},\"requestState\":\"$S1\"}}")
S2=$(echo "$R2" | jq -r '.result.requestState')

# Round 3: retry with ONLY step2 — step1 already rides inside requestState.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"test_incomplete_result_multi_round\",\"arguments\":{},\"inputResponses\":{\"step2\":{\"action\":\"accept\",\"content\":{\"color\":\"Alice\"}}},\"requestState\":\"$S2\"}}" \
  | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// Same one-liner — CallToolWithInputs drives all three rounds. The wire
// only ever ships the latest round's inputResponses; prior answers ride
// along inside the signed requestState, so handlers stay stateless.
res, _ := client.CallToolWithInputs(context.Background(), c,
    "test_incomplete_result_multi_round", map[string]any{},
    client.DefaultInputHandler(c),
)
// res.Sync.Content[0].Text holds the final greeting.`),
		).
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

	demo.Step("Tracing: rounds 2+ link back to round 1 (SEP-414 P6)").
		Note("`CallToolWithInputs` captures round 1's outbound traceparent and stamps it onto round 2+ as `_meta.io.modelcontextprotocol/tracelink` (SEP-414 P6 / issue 682). The server's trace middleware reads the link and calls `AddLink` on the round-N dispatch span, stitching the logical operation across separate W3C traces. **Star semantic** — every round 2+ links to round 1, not the previous round, so Tempo / Jaeger / Honeycomb show round 1 as the anchor regardless of how deep the loop goes.").
		Note("To see the stitched trace, bring up the LGTM observability stack alongside the demo: `(cd ../../docker/observability && docker compose up -d)` then `EXPORTER=auto make serve` here. Open Grafana → Tempo → service `mrtr-demo`. Click any round-2/3 dispatch span's Link entry — one click navigates to round 1's trace tree, looking at the original input-required handoff that the final ToolResult resolved from.").
		Note("Without this PR the same operation produced N unrelated traces — operators looking at round N had no way to navigate to round 1. The vendor-namespaced `_meta.io.modelcontextprotocol/tracelink` field is mcpkit-specific today; upstream WG standardization of a bare cross-SDK name is a future-discussion item.")

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

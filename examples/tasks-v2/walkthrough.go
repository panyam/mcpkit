package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// filterFlags strips top-level flags so the inner flag.Parse on -addr is happy.
func filterFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch a {
		case "--serve", "--tui", "--readme", "--non-interactive":
			continue
		case "--url":
			skip = true
			continue
		}
		out = append(out, a)
	}
	return out
}

func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}

	demo := demokit.New("MCP Tasks v2 (SEP-2663) — Server-Directed Async + MRTR").
		Dir("tasks-v2").
		Description("Walks through the v2 Tasks extension where the *server* decides whether to create a task — clients no longer send a task hint. Polymorphic tools/call, inlined results, ack-only cancel, and the new tasks/update flow that closes the elicit/sample (MRTR) loop.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve        # tasks-v2 server on :8080",
		"Terminal 2:  make demo         # this demo",
		"```",
	)

	demo.Section("v1 vs v2 — what changed",
		"v1 (SEP-1036, MCP spec 2025-11-25) had the *client* hint at task vs sync via a `task` param. v2 (SEP-2663, in-flight) flips the contract:",
		"",
		"- **Tasks is an extension** (`io.modelcontextprotocol/tasks`). Clients declare support during `initialize`; servers gate every task-creating `tools/call` and every `tasks/*` method on the negotiation.",
		"- **No client task hint.** Just call `tools/call` normally — the *server* decides whether to run sync or create a task. The client `client.ToolCall` helper returns a polymorphic `ToolCallResult` with either `Sync` or `Task` populated.",
		"- **`result_type` discriminator** on `tools/call` response: `\"task\"` means a task was created; absent means sync.",
		"- **`tasks/get` returns `DetailedTask`** with inlined `result` / `error` / `inputRequests` / `requestState` per status. No separate `tasks/result` round-trip.",
		"- **`tasks/cancel` returns an empty ack**. Observe the resulting `cancelled` status with the next `tasks/get`.",
		"- **`tasks/update` is the SEP-2663 resume path** for MRTR input rounds — the client delivers `inputResponses` keyed to whatever `inputRequests` the server emitted.",
		"- **Wire fields renamed**: `ttlSeconds`, `pollIntervalMilliseconds`. `parentTaskId` removed.",
		"- **Mcp-Name HTTP header** (SEP-2243) carries the new taskId on task-creating responses.",
		"- **Error semantics**: tool errors → `status: completed, isError: true`. Protocol errors → `status: failed` + `error` object.",
		"- **`tasks/result` and `tasks/list` removed** — `tasks/get` is the single read endpoint.",
	)

	var c *client.Client

	// --- Step 1: Connect ---
	demo.Step("Connect to the v2 tasks server (declare extension)").
		Arrow("Host", "Server", "POST /mcp — initialize (declares io.modelcontextprotocol/tasks)").
		DashedArrow("Server", "Host", "serverInfo + tasks extension advertised under capabilities.extensions").
		Note("`client.WithTasksExtension()` adds `io.modelcontextprotocol/tasks` to ClientCapabilities.Extensions during initialize. Without that declaration, the v2 server falls through to synchronous tools/call and rejects tasks/* with -32601.").
		Run(func() (result *demokit.StepResult) {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "tasks-v2-host", Version: "1.0"},
				client.WithGetSSEStream(),
				client.WithTasksExtension(),
				client.WithNotificationCallback(func(method string, params any) {
					if method == "notifications/progress" {
						fmt.Fprintf(os.Stderr, "    [notif] progress: %v\n", params)
					}
				}),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			fmt.Printf("    Extension advertised: %v\n", c.ServerSupportsExtension(core.TasksExtensionID))
			return
		})

	// --- Step 2: Polymorphic tools/call — sync branch ---
	demo.Step("Sync call: greet — ToolCall returns Sync variant").
		Arrow("Host", "Server", "tools/call: greet {name: \"world\"}").
		DashedArrow("Server", "Host", "ToolResult (no result_type discriminator → ToolCallResult.Sync)").
		Note("`client.ToolCall(c, name, args)` returns a polymorphic `*ToolCallResult`. For sync tools (no Execution / TaskSupport=forbidden) the server returns a plain `ToolResult` and the helper sets `Sync` (not `Task`). Callers branch on `result.IsTask()`.").
		Run(func() (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "greet", map[string]any{"name": "world"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if res.IsTask() {
				fmt.Printf("    UNEXPECTED: greet returned a task (%s)\n", res.Task.Task.TaskID)
				return
			}
			if len(res.Sync.Content) > 0 {
				fmt.Printf("    Sync result: %s\n", res.Sync.Content[0].Text)
			}
			return
		})

	// --- Step 3: Polymorphic tools/call — task branch ---
	demo.Step("slow_compute (no task hint!) — server creates a task → ToolCall returns Task variant").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds: 3}").
		DashedArrow("Server", "Host", "{result_type: \"task\", task: {taskId, status: working, ttlSeconds, ...}}\n+ Mcp-Name: <taskId> response header (SEP-2243)").
		Note("Critical v2 semantics: no `task` param in the request — the server elects to create a task because slow_compute has TaskSupport=optional. The discriminator `result_type: \"task\"` lights up `result.IsTask()` on the helper. The Mcp-Name HTTP header carries the same taskId so HTTP routing/observability can key off it without parsing the body.").
		Run(func() (result *demokit.StepResult) {
			var slowTaskID string
			res, err := client.ToolCall(c, "slow_compute", map[string]any{"seconds": 3, "label": "demo"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if !res.IsTask() {
				fmt.Printf("    UNEXPECTED: slow_compute returned sync\n")
				return
			}
			slowTaskID = res.Task.Task.TaskID
			fmt.Printf("    result_type:   %s\n", res.Task.ResultType)
			fmt.Printf("    taskId:        %s\n", slowTaskID)
			fmt.Printf("    status:        %s\n", res.Task.Task.Status)
			if res.Task.Task.TTLSeconds != nil {
				fmt.Printf("    ttlSeconds:    %d\n", *res.Task.Task.TTLSeconds)
			}
			if res.Task.Task.PollIntervalMilliseconds != nil {
				fmt.Printf("    pollIntervalMilliseconds: %d\n", *res.Task.Task.PollIntervalMilliseconds)
			}

			// --- Step 4 (chained): WaitForTask honors PollIntervalMilliseconds ---
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fmt.Printf("    waiting for terminal via client.WaitForTask ...\n")
			final, err := client.WaitForTask(ctx, c, slowTaskID)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			if final.Result != nil && len(final.Result.Content) > 0 {
				fmt.Printf("    Inlined result: %s\n", final.Result.Content[0].Text)
			}
			return
		})

	// --- Step 5: Tool error → completed + isError:true ---
	demo.Step("failing_job → status: completed, result.isError: true (TOOL error semantics)").
		Arrow("Host", "Server", "tools/call: failing_job → CreateTaskResult").
		Arrow("Host", "Server", "WaitForTask polls tasks/get until terminal").
		DashedArrow("Server", "Host", "{status: completed, result: {isError: true, content: [...]}}").
		Note("In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. Distinct from protocol failures (next step).").
		Run(func() (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "failing_job", map[string]any{})
			if err != nil || !res.IsTask() {
				fmt.Printf("    ERROR: %v / IsTask=%v\n", err, res != nil && res.IsTask())
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := client.WaitForTask(ctx, c, res.Task.Task.TaskID)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			if final.Result != nil {
				fmt.Printf("    result.isError:  %v\n", final.Result.IsError)
				if len(final.Result.Content) > 0 {
					fmt.Printf("    result text:     %s\n", final.Result.Content[0].Text)
				}
			}
			return
		})

	// --- Step 6: Protocol error → failed + error ---
	demo.Step("protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)").
		Arrow("Host", "Server", "tools/call: protocol_error_job → CreateTaskResult").
		Arrow("Host", "Server", "WaitForTask polls tasks/get until terminal").
		DashedArrow("Server", "Host", "{status: failed, error: {code, message}}").
		Note("Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring the JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.").
		Run(func() (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "protocol_error_job", map[string]any{})
			if err != nil || !res.IsTask() {
				fmt.Printf("    ERROR: %v / IsTask=%v\n", err, res != nil && res.IsTask())
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := client.WaitForTask(ctx, c, res.Task.Task.TaskID)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			if final.Error != nil {
				fmt.Printf("    error.code:      %d\n", final.Error.Code)
				fmt.Printf("    error.message:   %s\n", final.Error.Message)
			}
			return
		})

	// --- Step 7: MRTR — confirm_delete drives the elicit/update loop ---
	demo.Step("confirm_delete → input_required → tasks/update → completed (SEP-2663 MRTR)").
		Arrow("Host", "Server", "tools/call: confirm_delete {filename: \"important.txt\"}").
		DashedArrow("Server", "Host", "{result_type: task, task: {status: working, ...}}").
		Arrow("Host", "Server", "GetTask (polled until status = input_required)").
		DashedArrow("Server", "Host", "DetailedTask {status: input_required, inputRequests: { \"elicit-N\": {method, params} }}").
		Arrow("Host", "Server", "tasks/update {taskId, inputResponses: { \"elicit-N\": {action: accept, content: {confirm: true}} }}").
		DashedArrow("Server", "Host", "{} (empty ack)").
		Arrow("Host", "Server", "WaitForTask until terminal").
		DashedArrow("Server", "Host", "{status: completed, result: {content: [\"deleted 'important.txt'\"]}}").
		Note("This is the new SEP-2663 MRTR loop: the tool blocks on `TaskElicit`, the task parks in `input_required`, `tasks/get` surfaces the pending request under `inputRequests` (server-minted opaque keys), and `client.UpdateTask` delivers the matching response so the goroutine resumes. Cancellation during input_required propagates via ctx.Done() — see `TestV2_ElicitCancelUnblocks` in server tests.").
		Run(func() (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "confirm_delete", map[string]any{"filename": "important.txt"})
			if err != nil || !res.IsTask() {
				fmt.Printf("    ERROR: %v / IsTask=%v\n", err, res != nil && res.IsTask())
				return
			}
			taskID := res.Task.Task.TaskID
			fmt.Printf("    taskId: %s\n", taskID)

			// Poll until the task parks in input_required with a populated InputRequests map.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			deadline := time.Now().Add(5 * time.Second)
			var pending *core.DetailedTask
			for time.Now().Before(deadline) {
				dt, err := client.GetTask(c, taskID)
				if err != nil {
					fmt.Printf("    ERROR: %v\n", err)
					return
				}
				if dt.Status == core.TaskInputRequired && len(dt.InputRequests) > 0 {
					pending = dt
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if pending == nil {
				fmt.Printf("    ERROR: task did not reach input_required\n")
				return
			}

			// Server picks the key — clients MUST treat it as opaque echo.
			var key string
			for k := range pending.InputRequests {
				key = k
				break
			}
			fmt.Printf("    pending status:   %s\n", pending.Status)
			fmt.Printf("    pending key:      %s (server-minted, opaque)\n", key)
			fmt.Printf("    pending method:   %s\n", pending.InputRequests[key].Method)

			// Resume via tasks/update.
			fmt.Printf("    delivering tasks/update {action: accept, confirm: true} ...\n")
			if err := client.UpdateTask(c, core.UpdateTaskRequest{
				TaskID: taskID,
				InputResponses: core.InputResponses{
					key: json.RawMessage(`{"action":"accept","content":{"confirm":true}}`),
				},
				RequestState: pending.RequestState,
			}); err != nil {
				fmt.Printf("    ERROR tasks/update: %v\n", err)
				return
			}

			// Wait for completion.
			final, err := client.WaitForTask(ctx, c, taskID)
			if err != nil {
				fmt.Printf("    ERROR WaitForTask: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status:  %s\n", final.Status)
			if final.Result != nil && len(final.Result.Content) > 0 {
				fmt.Printf("    Inlined result:   %s\n", final.Result.Content[0].Text)
			}
			return
		})

	// --- Step 8: Cancellation — ack-only ---
	demo.Step("Cancel a long-running task → empty ack, status settles to cancelled").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds: 10}").
		DashedArrow("Server", "Host", "{result_type: task, task: ...}").
		Arrow("Host", "Server", "client.CancelTask").
		DashedArrow("Server", "Host", "{} (empty ack — SEP-2663 cancel returns no task state)").
		Arrow("Host", "Server", "WaitForTask polls tasks/get").
		DashedArrow("Server", "Host", "{status: cancelled}").
		Note("Same cooperative cancellation as v1 (server cancels the goroutine context; tools that select on ctx.Done() exit cleanly), but the response shape changed: SEP-2663 cancel returns an empty `{}` ack. Observe the `cancelled` status via the next `tasks/get` (or `WaitForTask` which does it for you).").
		Run(func() (result *demokit.StepResult) {
			res, err := client.ToolCall(c, "slow_compute", map[string]any{"seconds": 10, "label": "to-cancel"})
			if err != nil || !res.IsTask() {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			cancelID := res.Task.Task.TaskID
			fmt.Printf("    Started taskId: %s\n", cancelID)

			time.Sleep(500 * time.Millisecond)
			fmt.Printf("    Cancelling ...\n")
			if err := client.CancelTask(c, cancelID); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := client.WaitForTask(ctx, c, cancelID)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			return
		})

	demo.Section("Where each piece lives in mcpkit",
		"- v2 server library: `server/tasks_v2.go` (RegisterTasks, gating, MRTR runtime)",
		"- v2 wire types (`CreateTaskResult`, `DetailedTask`, `TaskInfoV2`, `UpdateTaskRequest`, `ResultTypeTask`): `core/task_v2.go` (SEP-2663)",
		"- v2 client helpers (`ToolCall`, `GetTask`, `UpdateTask`, `WaitForTask`, `CancelTask`): `client/tasks.go`",
		"- Conformance tests: `conformance/tasks-v2/scenarios.test.ts`",
		"- Implementation plan + open questions: `PLAN.md`",
	)

	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

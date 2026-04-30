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

	demo := demokit.New("MCP Tasks v2 (SEP-2557) — Server-Directed Async").
		Dir("tasks-v2").
		Description("Walks through the v2 Tasks protocol where the *server* decides whether to create a task — clients no longer send a task hint. Inlined results, flat shape, and tool-error vs protocol-error semantics.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve        # tasks-v2 server on :8080",
		"Terminal 2:  make run          # this demo",
		"```",
	)

	demo.Section("v1 vs v2 — what changed",
		"In v1 (SEP-1036) the *client* hints at task vs sync via a `task` param. v2 (SEP-2557) flips this:",
		"",
		"- **No client task hint.** Just call `tools/call` normally — the *server* decides.",
		"- **`resultType` discriminator** on `tools/call` response: `\"task\"` means a task was created (poll); absent means sync result.",
		"- **Inlined `result` / `error` / `inputRequests`** on `tasks/get` — no separate `tasks/result` call.",
		"- **TTL in seconds** (was milliseconds in v1).",
		"- **Error semantics**: tool errors (logic failures) → `status: completed, isError: true`. Protocol errors (framework crashes) → `status: failed` + `error` object.",
		"- **`tasks/result` and `tasks/list` removed** — `tasks/get` is the single read endpoint.",
	)

	var (
		c          *client.Client
		slowTaskID string
		failTaskID string
	)

	// --- Step 1: Connect ---
	demo.Step("Connect to the v2 tasks server").
		Arrow("Host", "Server", "POST /mcp — initialize (declares io.modelcontextprotocol/tasks)").
		DashedArrow("Server", "Host", "serverInfo + tasks extension advertised").
		Note("The mcpkit client opens a GET SSE stream so progress notifications reach us during polling. Initialize declares support for the SEP-2663 tasks extension; without that declaration the v2 server falls through to synchronous tools/call and rejects tasks/* with -32601.").
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
			return
		})

	// --- Step 2: Sync call (resultType absent) ---
	demo.Step("Sync call: greet — no task created").
		Arrow("Host", "Server", "tools/call: greet {name: \"world\"}").
		DashedArrow("Server", "Host", "ToolResult (no resultType discriminator → sync)").
		Note("greet is taskSupport=forbidden. The server runs it inline and returns the standard ToolResult shape. No task created. The host can detect sync vs task by checking for the `resultType: \"task\"` discriminator on the response.").
		Run(func() (result *demokit.StepResult) {
			text, err := c.ToolCall("greet", map[string]any{"name": "world"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			return
		})

	// --- Step 3: Server-decided task (slow_compute, no client hint) ---
	demo.Step("slow_compute (no task hint!) — server creates a task").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds: 3}").
		DashedArrow("Server", "Host", "{resultType: \"task\", task: {taskId, status: working, ttl}}").
		Note("Critical v2 semantics: client doesn't include a `task` param — calls slow_compute like any sync tool. Because slow_compute has Execution.TaskSupport=optional, the server elects to create a task. The discriminator `resultType: \"task\"` tells the host to switch to polling mode.").
		Run(func() (result *demokit.StepResult) {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "slow_compute",
				"arguments": map[string]any{"seconds": 3, "label": "demo"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var ctr core.CreateTaskResult
			if err := json.Unmarshal(res.Raw, &ctr); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if ctr.ResultType != core.ResultTypeTask {
				fmt.Printf("    UNEXPECTED: missing resultType=\"task\" discriminator\n")
				return
			}
			slowTaskID = ctr.Task.TaskID
			fmt.Printf("    resultType:    %s\n", ctr.ResultType)
			fmt.Printf("    taskId:        %s\n", slowTaskID)
			fmt.Printf("    status:        %s\n", ctr.Task.Status)
			if ctr.Task.TTLSeconds != nil {
				fmt.Printf("    ttlSeconds:    %d (SEP-2663 wire field)\n", *ctr.Task.TTLSeconds)
			}
			return
		})

	// --- Step 4: tasks/get with inlined result on completion ---
	demo.Step("Poll tasks/get — final response inlines the result").
		Arrow("Host", "Server", "tasks/get {taskId}  (polled)").
		DashedArrow("Server", "Host", "notifications/progress (1/3, 2/3, 3/3) via SSE").
		DashedArrow("Server", "Host", "{status: completed, result: {...}, ttl: ...}  (no separate tasks/result needed)").
		Note("v2's flat shape: DetailedTask has the v2 task fields (taskId, status, ttlSeconds, ...) at the top level, and inlines the actual ToolResult under `result` once status is terminal. No second roundtrip to tasks/result.").
		Run(func() (result *demokit.StepResult) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			final, err := pollV2(ctx, c, slowTaskID, 500*time.Millisecond)
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
		Arrow("Host", "Server", "tools/call: failing_job").
		DashedArrow("Server", "Host", "{resultType: task, task: ...}").
		Arrow("Host", "Server", "tasks/get (polled)").
		DashedArrow("Server", "Host", "{status: completed, result: {isError: true, content: [...]}}").
		Note("In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. This is distinct from protocol failures (next step).").
		Run(func() (result *demokit.StepResult) {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "failing_job",
				"arguments": map[string]any{},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var ctr core.CreateTaskResult
			json.Unmarshal(res.Raw, &ctr)
			failTaskID = ctr.Task.TaskID

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := pollV2(ctx, c, failTaskID, 200*time.Millisecond)
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
		Arrow("Host", "Server", "tools/call: protocol_error_job").
		Arrow("Host", "Server", "tasks/get (polled)").
		DashedArrow("Server", "Host", "{status: failed, error: {code, message}}").
		Note("Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.").
		Run(func() (result *demokit.StepResult) {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "protocol_error_job",
				"arguments": map[string]any{},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var ctr core.CreateTaskResult
			json.Unmarshal(res.Raw, &ctr)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := pollV2(ctx, c, ctr.Task.TaskID, 200*time.Millisecond)
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

	// --- Step 7: Cancellation ---
	demo.Step("Cancel a long-running task → status: cancelled").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds: 10}").
		DashedArrow("Server", "Host", "{resultType: task, task: ...}").
		Arrow("Host", "Server", "tasks/cancel {taskId}").
		Arrow("Host", "Server", "tasks/get (final)").
		DashedArrow("Server", "Host", "{status: cancelled}").
		Note("Same cooperative cancellation as v1. Server cancels the goroutine context; tools that select on ctx.Done() exit cleanly. v2 cancel response also includes the flat TaskInfo so the host doesn't need an extra round-trip.").
		Run(func() (result *demokit.StepResult) {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "slow_compute",
				"arguments": map[string]any{"seconds": 10, "label": "to-cancel"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var ctr core.CreateTaskResult
			json.Unmarshal(res.Raw, &ctr)
			cancelID := ctr.Task.TaskID
			fmt.Printf("    Started taskId: %s\n", cancelID)

			time.Sleep(500 * time.Millisecond)
			fmt.Printf("    Cancelling ...\n")
			if _, err := c.Call("tasks/cancel", map[string]any{"taskId": cancelID}); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := pollV2(ctx, c, cancelID, 200*time.Millisecond)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			return
		})

	demo.Section("Where each piece lives in mcpkit",
		"- v2 server library: `server/tasks_v2.go`",
		"- v2 wire types (`CreateTaskResult`, `DetailedTask`, `TaskInfoV2`, `ResultTypeTask`): `core/task_v2.go` (SEP-2663)",
		"- Conformance tests: `conformance/tasks-v2/scenarios.test.ts` (21 scenarios)",
		"- Implementation plan: `docs/TASKS_V2_PLAN.md`",
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

// pollV2 polls tasks/get until the task reaches a terminal status or the
// context expires. There's no v2 client helper in mcpkit yet (v1 has
// client.WaitForTask), so this is inline.
func pollV2(ctx context.Context, c *client.Client, taskID string, interval time.Duration) (*core.DetailedTask, error) {
	for {
		res, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
		if err != nil {
			return nil, err
		}
		var got core.DetailedTask
		if err := json.Unmarshal(res.Raw, &got); err != nil {
			return nil, err
		}
		if got.Status.IsTerminal() {
			return &got, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

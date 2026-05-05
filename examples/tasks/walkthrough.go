package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

func runDemo() {
	serverURL := common.ServerURL()

	demo := demokit.New("MCP Tasks — Async Tool Execution Lifecycle").
		Dir("tasks").
		Description("Walks through the MCP Tasks (SEP-1036) lifecycle: optional/required task support, polling, progress notifications, and cancellation.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve        # tasks server on :8080",
		"Terminal 2:  make run          # this demo",
		"```",
	)

	demo.Section("How tasks work",
		"Each tool's `Execution.TaskSupport` declares whether it can run as a task:",
		"",
		"- **forbidden** (or absent): sync-only. Calling with a `task` hint returns an error.",
		"- **optional**: client chooses. `tools/call` blocks for the result; `tools/call` with `task` hint returns a `CreateTaskResult` immediately.",
		"- **required**: must be invoked as a task. Direct sync calls return an error.",
		"",
		"For task invocations, the server responds with `{taskId, ttl, pollInterval}`. The host polls `tasks/get` until the status is terminal (`completed`, `failed`, or `cancelled`), then fetches the result via `tasks/result`. Progress notifications stream over the session's GET SSE channel.",
	)

	var (
		c            *client.Client
		slowTaskID   string
		cancelTaskID string
	)

	// --- Step 1: Connect ---
	demo.Step("Connect to the MCP server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + Mcp-Session-Id + tasks capability").
		Note("The server advertises tasks capability in initialize. The mcpkit client opens a GET SSE stream so server-pushed notifications (progress, status changes) reach us during polling.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "tasks-demo-host", Version: "1.0"},
				client.WithGetSSEStream(),
				client.WithNotificationCallback(func(method string, params any) {
					// Show progress notifications inline while we poll.
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

			tools, _ := c.ListTools()
			fmt.Printf("\n    Tools (with task support metadata):\n")
			for _, t := range tools {
				support := "forbidden"
				if t.Execution != nil {
					support = string(t.Execution.TaskSupport)
				}
				fmt.Printf("      - %-15s taskSupport=%s\n", t.Name, support)
			}
			return
		})

	// --- Step 2: Sync call (no task) ---
	demo.Step("Sync call: greet (taskSupport=forbidden)").
		Arrow("Host", "Server", "tools/call: greet {name: \"world\"}  (no task hint)").
		DashedArrow("Server", "Host", "ToolResult immediately").
		Note("greet is sync-only. The result returns directly in the tools/call response — no task created. This is the path most existing tools use today; tasks are opt-in per tool.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			text, err := c.ToolCall("greet", map[string]any{"name": "world"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			return
		})

	// --- Step 3: Optional task — async invocation ---
	demo.Step("Optional task: slow_compute as task → CreateTaskResult").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds:3} + task: {ttl: 60s}").
		DashedArrow("Server", "Host", "{taskId, status: working, ttl, pollInterval}").
		Note("slow_compute has taskSupport=optional. Sending the `task` hint tells the server to run it asynchronously. We get a taskId back immediately while the work runs in a background goroutine.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			res, err := client.ToolCallAsTaskV1(c, "slow_compute", map[string]any{
				"seconds": 3, "label": "demo",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			slowTaskID = res.Task.TaskID
			fmt.Printf("    taskId:        %s\n", slowTaskID)
			fmt.Printf("    status:        %s\n", res.Task.Status)
			fmt.Printf("    pollInterval:  %dms\n", res.Task.PollInterval)
			return
		})

	// --- Step 4: Poll + receive progress notifications ---
	demo.Step("Poll tasks/get until terminal — receive notifications/progress").
		Arrow("Host", "Server", "tasks/get {taskId}  (polled every pollInterval)").
		DashedArrow("Server", "Host", "notifications/progress (1/3, 2/3, 3/3) via SSE").
		DashedArrow("Server", "Host", "{status: completed} on terminal poll").
		Note("The server streams progress notifications over the GET SSE channel while the task runs. Our notification callback (set up in Step 1) prints them inline. Once status reaches `completed`, the polling stops.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			final, err := client.WaitForTaskV1(bgCtx, c, slowTaskID, 500*time.Millisecond)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			return
		})

	// --- Step 5: Fetch the payload ---
	demo.Step("Fetch the result payload via tasks/result").
		Arrow("Host", "Server", "tasks/result {taskId}").
		DashedArrow("Server", "Host", "ToolResult").
		Note("tasks/get returns task status only. To get the actual tool result (content blocks, isError flag, structured content), the host calls tasks/result with the same taskId.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			tr, _, err := client.GetTaskPayloadV1(c, slowTaskID)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if len(tr.Content) > 0 {
				fmt.Printf("    Result: %s\n", tr.Content[0].Text)
			}
			return
		})

	// --- Step 6: Required task — failing_job ---
	demo.Step("Required task: failing_job — sync call returns an error").
		Arrow("Host", "Server", "tools/call: failing_job  (no task hint)").
		DashedArrow("Server", "Host", "JSON-RPC error (taskSupport=required)").
		Note("failing_job declares Execution.TaskSupport=required. Sync invocation returns an error telling the host to retry with a task hint. This guards expensive/long tools from blocking the request thread.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.ToolCall("failing_job", map[string]any{})
			if err == nil {
				fmt.Printf("    UNEXPECTED: sync call succeeded\n")
				return
			}
			fmt.Printf("    Server returned: %v\n", err)
			return
		})

	// --- Step 7: failing_job as task → status: failed ---
	demo.Step("Invoke failing_job as task → terminal status: failed").
		Arrow("Host", "Server", "tools/call: failing_job + task hint").
		DashedArrow("Server", "Host", "{taskId, status: working}").
		Arrow("Host", "Server", "tasks/get (polled)").
		DashedArrow("Server", "Host", "{status: failed, error: \"simulated failure\"}").
		Note("Errors from required-task tools surface as a terminal status of `failed`. The host gets the taskId immediately, polls, and learns the task failed via the status field — no exception thrown on the polling call.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			res, err := client.ToolCallAsTaskV1(c, "failing_job", map[string]any{})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := client.WaitForTaskV1(bgCtx, c, res.Task.TaskID, 500*time.Millisecond)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			return
		})

	// --- Step 8: Cancellation ---
	demo.Step("Cancel a long-running task mid-flight").
		Arrow("Host", "Server", "tools/call: slow_compute {seconds: 10} + task hint").
		DashedArrow("Server", "Host", "{taskId, status: working}").
		Arrow("Host", "Server", "tasks/cancel {taskId}").
		DashedArrow("Server", "Host", "ack").
		Arrow("Host", "Server", "tasks/get (final)").
		DashedArrow("Server", "Host", "{status: cancelled}").
		Note("Tasks support cooperative cancellation. The server cancels the goroutine's context; tools that select on ctx.Done() exit cleanly. Status transitions to `cancelled`. mcpkit guards against terminal-to-terminal transitions so a tool finishing normally after cancel doesn't overwrite the cancelled status.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			res, err := client.ToolCallAsTaskV1(c, "slow_compute", map[string]any{
				"seconds": 10, "label": "to-cancel",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			cancelTaskID = res.Task.TaskID
			fmt.Printf("    Started taskId: %s\n", cancelTaskID)

			time.Sleep(500 * time.Millisecond)
			fmt.Printf("    Cancelling ...\n")
			if _, err := client.CancelTaskV1(c, cancelTaskID); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			final, err := client.WaitForTaskV1(bgCtx, c, cancelTaskID, 200*time.Millisecond)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Terminal status: %s\n", final.Status)
			return
		})

	demo.Section("Where each piece lives in mcpkit",
		"- Tasks server library: `server/task_*.go`, `server/tasks_experimental.go`",
		"- TaskContext (used by required-task tools): `core.TaskContext` — `core/task.go`",
		"- Client helpers: `client/tasks.go` — `ToolCallAsTask`, `WaitForTask`, `GetTask`, `GetTaskPayload`, `CancelTask`",
		"- Tool declares task support via `core.ToolDef.Execution.TaskSupport` (`forbidden` | `optional` | `required`)",
		"",
		"For elicitation/sampling from inside a task (the `confirm_delete` and `write_haiku` tools also registered on this server), see `examples/tasks/run-exercises.sh`.",
	)

	common.SetupRenderer(demo)

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

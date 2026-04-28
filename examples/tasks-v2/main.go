// Example: MCP Tasks v2 (SEP-2557) — server-directed async tool execution.
//
// Demonstrates the v2 tasks protocol where the server decides whether to
// create a task — clients do not send a task hint.
//
// Tools:
//   - greet:              sync-only (no Execution field = forbidden)
//   - slow_compute:       optional task support (server creates task for async)
//   - failing_job:        required task support (tool error → completed + isError)
//   - protocol_error_job: required task support (protocol error → failed + error)
//   - external_job:       required task support (TaskCallbacks proxy pattern)
//
// Run:  go run . -addr :8080
// Test: SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	srv := server.NewServer(
		core.ServerInfo{Name: "tasks-v2-demo", Version: "0.1.0"},
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)

	// greet: sync-only tool. No Execution field = taskSupport forbidden.
	type greetInput struct {
		Name string `json:"name" jsonschema:"description=Name to greet,required"`
	}
	srv.Register(core.TextTool[greetInput]("greet", "Greet someone (sync-only, no task support)",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return fmt.Sprintf("Hello, %s!", input.Name), nil
		},
	))

	// slow_compute: optional task support. In v2, server always creates a task
	// for this tool (no client hint needed). Immediate result shortcut for 0s.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_compute",
			Description: "Simulate a slow computation. In v2, always runs as a task unless instant (0 seconds).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"seconds": map[string]any{
						"type":        "integer",
						"description": "How many seconds to compute (sleep)",
						"default":     3,
					},
					"label": map[string]any{
						"type":        "string",
						"description": "A label for the computation",
						"default":     "default",
					},
				},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Seconds int    `json:"seconds"`
				Label   string `json:"label"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.Label == "" {
				args.Label = "default"
			}

			// Immediate result shortcut: 0 seconds means instant.
			if args.Seconds <= 0 {
				return core.TextResult(fmt.Sprintf("Computation %q completed instantly. Result: 42.", args.Label)), nil
			}

			log.Printf("[slow_compute] starting %q: sleeping %ds...", args.Label, args.Seconds)
			var progressToken any
			if tc := server.GetTaskContext(ctx); tc != nil {
				progressToken = tc.ProgressToken()
				if progressToken == nil {
					progressToken = tc.TaskID()
				}
			}
			for i := 1; i <= args.Seconds; i++ {
				select {
				case <-ctx.Done():
					log.Printf("[slow_compute] cancelled %q at %d/%d", args.Label, i, args.Seconds)
					return core.TextResult(fmt.Sprintf("Computation %q cancelled at %d/%d.", args.Label, i, args.Seconds)), nil
				case <-time.After(1 * time.Second):
					ctx.EmitProgress(progressToken, float64(i), float64(args.Seconds), fmt.Sprintf("%s: %d/%d", args.Label, i, args.Seconds))
				}
			}
			log.Printf("[slow_compute] finished %q", args.Label)
			return core.TextResult(fmt.Sprintf("Computation %q completed after %d seconds. Result: 42.", args.Label, args.Seconds)), nil
		},
	)

	// failing_job: required task support. Always fails with a tool execution error.
	// In v2, tool errors → completed + isError:true (NOT failed).
	srv.RegisterTool(
		core.ToolDef{
			Name:        "failing_job",
			Description: "A job that always fails after 1 second. In v2: tool error = completed + isError:true.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			log.Printf("[failing_job] starting (will fail in 1s)...")
			time.Sleep(1 * time.Second)
			return core.ToolResult{}, fmt.Errorf("simulated failure: job crashed")
		},
	)

	// protocol_error_job: required task support. Triggers a protocol-level failure
	// by panicking. In v2, protocol errors → failed + error field.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "protocol_error_job",
			Description: "A job that triggers a protocol-level error (panic). In v2: failed + error field.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			log.Printf("[protocol_error_job] starting (will panic in 500ms)...")
			time.Sleep(500 * time.Millisecond)
			panic("simulated protocol error: server internal failure")
		},
	)

	// external_job: required task support with TaskCallbacks (proxy pattern).
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "external_job",
			Description: "Simulates an external job system with custom task state lookup.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job_id": map[string]any{
						"type":        "string",
						"description": "External job ID to track",
						"default":     "job-001",
					},
				},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				JobID string `json:"job_id"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.JobID == "" {
				args.JobID = "job-001"
			}
			log.Printf("[external_job] started external job %s", args.JobID)
			time.Sleep(1 * time.Second)
			log.Printf("[external_job] external job %s completed", args.JobID)
			return core.TextResult(fmt.Sprintf("External job %s completed", args.JobID)), nil
		},
		TaskCallbacks: &server.TaskCallbacks{
			GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResult, bool) {
				log.Printf("[external_job] custom getTask for %s", taskID)
				return core.GetTaskResult{}, false
			},
			GetResult: func(ctx core.MethodContext, taskID string) (core.ToolResult, bool) {
				log.Printf("[external_job] custom getResult for %s", taskID)
				return core.ToolResult{}, false
			},
		},
	})

	// Register v2 tasks on the server.
	server.RegisterTasksV2(server.TasksV2Config{Server: srv})

	log.Printf("Tasks v2 demo server on %s", *addr)
	log.Printf("Connect: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  greet              — sync-only")
	log.Printf("  slow_compute       — optional task (server-directed)")
	log.Printf("  failing_job        — required task (tool error → completed + isError)")
	log.Printf("  protocol_error_job — required task (protocol error → failed + error)")
	log.Printf("  external_job       �� required task (TaskCallbacks proxy)")
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}

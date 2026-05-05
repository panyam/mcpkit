// Example: MCP Tasks — async tool execution with lifecycle tracking.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP Tasks server on :8080
//	Terminal 2:  make run           # demokit walkthrough
//
// Tools:
//   - greet:          sync-only (no Execution field = forbidden per spec)
//   - slow_compute:   optional task support (client chooses sync or async)
//   - failing_job:    required task support (must be invoked as task)
//   - confirm_delete: required task + elicitation (asks user before deleting)
//   - write_haiku:    required task + sampling (asks LLM to write a haiku)
//   - external_job:   required task + TaskCallbacks (external proxy pattern)
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
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	srv := server.NewServer(
		core.ServerInfo{Name: "tasks-demo", Version: "0.1.0"},
		common.MCPServerOptions(*addr, "[mcp] ")...,
	)

	// greet: sync-only tool. No Execution field means taskSupport = forbidden.
	// Calling with a task hint will return an error.
	type greetInput struct {
		Name string `json:"name" jsonschema:"description=Name to greet,required"`
	}
	srv.Register(core.TextTool[greetInput]("greet", "Greet someone (sync-only, no task support)",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return fmt.Sprintf("Hello, %s!", input.Name), nil
		},
	))

	// slow_compute: optional task support. Can be called sync (blocks) or
	// async (returns task immediately, poll for result).
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_compute",
			Description: "Simulate a slow computation (sleeps for the given duration). Supports optional async task execution.",
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
			if args.Seconds <= 0 {
				args.Seconds = 3
			}
			if args.Label == "" {
				args.Label = "default"
			}

			log.Printf("[slow_compute] starting %q: sleeping %ds...", args.Label, args.Seconds)
			// Emit progress notifications per second. For async tasks,
			// DetachForBackground replaces the notifyFunc with the session-level
			// one so notifications reach the client via GET SSE.
			// Use the task ID as the progress token if running as a task.
			// Use the client's progressToken if available, fall back to taskID.
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
					// Task was cancelled (Phase 5) — exit early.
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

	// failing_job: required task support. Must be invoked as a task.
	// Calling without a task hint returns an error. Always fails after a delay.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "failing_job",
			Description: "A job that always fails after 1 second. Requires task invocation — calling without 'task' hint returns an error.",
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

	// confirm_delete: demonstrates elicitation from a background task.
	// Requires task invocation. Uses TaskElicit to ask the user for confirmation
	// before "deleting" a file.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "confirm_delete",
			Description: "Asks for confirmation before deleting a file. Demonstrates task-based elicitation.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "File to delete",
						"default":     "important.txt",
					},
				},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := server.GetTaskContext(ctx)
			if tc == nil {
				return core.ToolResult{}, fmt.Errorf("confirm_delete requires task context")
			}

			var args struct {
				Filename string `json:"filename"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.Filename == "" {
				args.Filename = "important.txt"
			}

			log.Printf("[confirm_delete] asking about %q", args.Filename)
			result, err := tc.TaskElicit(core.ElicitationRequest{
				Message:         fmt.Sprintf("Are you sure you want to delete '%s'?", args.Filename),
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`),
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("Elicitation failed: %v", err)), nil
			}

			if result.Action == "accept" {
				confirmed, _ := result.Content["confirm"].(bool)
				if confirmed {
					log.Printf("[confirm_delete] user confirmed deletion of %q", args.Filename)
					return core.TextResult(fmt.Sprintf("Deleted '%s'", args.Filename)), nil
				}
			}
			log.Printf("[confirm_delete] user declined deletion of %q", args.Filename)
			return core.TextResult("Deletion cancelled"), nil
		},
	)

	// write_haiku: demonstrates sampling from a background task.
	// Requires task invocation. Uses TaskSample to ask the LLM to generate a haiku.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "write_haiku",
			Description: "Asks the LLM to write a haiku on a topic. Demonstrates task-based sampling.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type":        "string",
						"description": "Topic for the haiku",
						"default":     "nature",
					},
				},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := server.GetTaskContext(ctx)
			if tc == nil {
				return core.ToolResult{}, fmt.Errorf("write_haiku requires task context")
			}

			var args struct {
				Topic string `json:"topic"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.Topic == "" {
				args.Topic = "nature"
			}

			log.Printf("[write_haiku] requesting haiku about %q", args.Topic)
			result, err := tc.TaskSample(core.CreateMessageRequest{
				Messages: []core.SamplingMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: fmt.Sprintf("Write a haiku about %s", args.Topic)},
				}},
				MaxTokens: 50,
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("Sampling failed: %v", err)), nil
			}

			log.Printf("[write_haiku] received haiku from %s", result.Model)
			return core.TextResult(fmt.Sprintf("Haiku about %s:\n%s", args.Topic, result.Content.Text)), nil
		},
	)

	// external_job: demonstrates TaskCallbacks (per-tool getTask/getResult overrides).
	// Simulates proxying an external job system where the tool provides custom
	// task state lookup instead of relying on the default TaskStore.
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "external_job",
			Description: "Simulates an external job system with custom task state lookup. Demonstrates TaskCallbacks for the external proxy pattern.",
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
			GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResultV1, bool) {
				// In a real system, this would query an external API (Step Functions, etc.)
				// For demo purposes, we augment the status message.
				log.Printf("[external_job] custom getTask for %s", taskID)
				return core.GetTaskResultV1{}, false // fall through to store
			},
			GetResult: func(ctx core.MethodContext, taskID string) (core.ToolResult, bool) {
				// In a real system, this would fetch the result from the external system.
				log.Printf("[external_job] custom getResult for %s", taskID)
				return core.ToolResult{}, false // fall through to store
			},
		},
	})

	// Register v1 tasks capability on the server.
	server.RegisterTasksV1(server.TasksConfigV1{Server: srv})

	log.Printf("Tasks demo server on %s", *addr)
	log.Printf("Connect MCPJam or VS Code: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  greet          — sync-only (no task support)")
	log.Printf("  slow_compute   — optional task support (try with/without 'task' hint)")
	log.Printf("  failing_job    — required task support (must include 'task' hint)")
	log.Printf("  confirm_delete — required task + elicitation (asks user before deleting)")
	log.Printf("  write_haiku    — required task + sampling (asks LLM to write a haiku)")
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}

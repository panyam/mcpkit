// Example: MCP Tasks — async tool execution with lifecycle tracking.
//
// Demonstrates three tools with different task support modes:
//   - greet:        sync-only (no Execution field = forbidden per spec)
//   - slow_compute: optional task support (client chooses sync or async)
//   - failing_job:  required task support (must be invoked as task)
//
// Run:  go run . -addr :8080
// Connect MCPJam or VS Code to http://localhost:8080/mcp
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	srv := server.NewServer(
		core.ServerInfo{Name: "tasks-demo", Version: "0.1.0"},
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
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
			time.Sleep(time.Duration(args.Seconds) * time.Second)
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

	// Register tasks capability on the server.
	tasks.Register(tasks.Config{Server: srv})

	log.Printf("Tasks demo server on %s", *addr)
	log.Printf("Connect MCPJam or VS Code: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  greet          — sync-only (no task support)")
	log.Printf("  slow_compute   — optional task support (try with/without 'task' hint)")
	log.Printf("  failing_job    — required task support (must include 'task' hint)")
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}

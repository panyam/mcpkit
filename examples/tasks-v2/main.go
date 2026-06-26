// Example: MCP Tasks v2 (SEP-2557) — server-directed async tool execution.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # tasks-v2 server on :8080
//	Terminal 2:  make run           # demokit walkthrough
//
// Tools:
//   - greet:              sync-only (no Execution field = forbidden)
//   - slow_compute:       optional task support (server creates task for async)
//   - failing_job:        required task support (tool error → completed + isError)
//   - protocol_error_job: required task support (protocol error → failed + error)
//   - external_job:       required task support (TaskCallbacks proxy pattern)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/ext/tasks"
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
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("tasks-v2-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	log.Printf("Connect: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  greet              — sync-only")
	log.Printf("  slow_compute       — optional task (server-directed)")
	log.Printf("  failing_job        — required task (tool error → completed + isError)")
	log.Printf("  confirm_delete     — required task (input_required → tasks/update → completed)")
	log.Printf("  multi_input        — required task (two simultaneous inputRequests for partial fulfillment)")
	log.Printf("  protocol_error_job — required task (protocol error → failed + error)")
	log.Printf("  external_job       — required task (TaskCallbacks proxy)")

	if err := common.RunServer(common.ServerConfig{
		Name:           "tasks-v2-demo",
		Addr:           *addr,
		TracerProvider: tp,
		Wire:           wire,
		Register: func(srv *server.Server) {
			registerTasksV2DemoTools(srv)
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func registerTasksV2DemoTools(srv *server.Server) {
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
	type slowComputeInput struct {
		Seconds int    `json:"seconds,omitempty"`
		Label   string `json:"label,omitempty"`
	}
	srv.Register(core.TypedTool[slowComputeInput, core.ToolResponse]("slow_compute",
		"Simulate a slow computation. In v2, always runs as a task unless instant (0 seconds).",
		func(ctx core.ToolContext, args slowComputeInput) (core.ToolResponse, error) {
			if args.Label == "" {
				args.Label = "default"
			}

			// Immediate result shortcut: 0 seconds means instant. The middleware
			// will wrap this as an already-completed task (taskSupport=optional
			// + sync return) so the wire shape stays CreateTaskResult.
			if args.Seconds <= 0 {
				return core.TextResult(fmt.Sprintf("Computation %q completed instantly. Result: 42.", args.Label)), nil
			}

			// SEP-2663 Option 2: the slow loop blocks and emits progress, both
			// of which must run inside the continuation goroutine (so the
			// G6 filter is active and the response isn't held open). On the
			// first sync pass there's no TaskContext, so signal GoAsync.
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}

			log.Printf("[slow_compute] starting %q: sleeping %ds...", args.Label, args.Seconds)
			var progressToken any
			if tc := tasks.GetTaskContext(ctx); tc != nil {
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
		core.WithInputSchemaOverride(map[string]any{
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
		}),
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportOptional}),
	))

	// failing_job: required task support. Always fails with a tool execution error.
	// In v2, tool errors → completed + isError:true (NOT failed).
	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("failing_job",
		"A job that always fails after 1 second. In v2: tool error = completed + isError:true.",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			log.Printf("[failing_job] starting (will fail in 1s)...")
			time.Sleep(1 * time.Second)
			return core.ToolResult{}, fmt.Errorf("simulated failure: job crashed")
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))

	// confirm_delete: required task support, demonstrates the SEP-2663 MRTR
	// elicit → tasks/update → complete loop. The tool calls TaskElicit, parks
	// the task in input_required (which surfaces inputRequests on tasks/get),
	// and resumes when the client sends the matching response via tasks/update.
	type confirmDeleteInput struct {
		Filename string `json:"filename,omitempty"`
	}
	srv.Register(core.TypedTool[confirmDeleteInput, core.ToolResponse]("confirm_delete",
		"Asks the client to confirm before deleting (demonstrates SEP-2663 inputRequests/inputResponses).",
		func(ctx core.ToolContext, args confirmDeleteInput) (core.ToolResponse, error) {
			tc := tasks.GetTaskContext(ctx)
			if tc == nil {
				// SEP-2663 Option 2: TaskElicit needs the continuation
				// goroutine's TaskContext.
				return core.GoAsyncResult{}, nil
			}
			if args.Filename == "" {
				args.Filename = "important.txt"
			}

			log.Printf("[confirm_delete] asking about %q (parking task in input_required)...", args.Filename)
			result, err := tc.TaskElicit(core.ElicitationRequest{
				Message:         fmt.Sprintf("Delete '%s'?", args.Filename),
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`),
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("elicitation failed: %v", err)), nil
			}
			if result.Action == "accept" {
				if confirmed, _ := result.Content["confirm"].(bool); confirmed {
					log.Printf("[confirm_delete] user confirmed; deleting %q", args.Filename)
					return core.TextResult(fmt.Sprintf("deleted '%s'", args.Filename)), nil
				}
			}
			log.Printf("[confirm_delete] user declined; keeping %q", args.Filename)
			return core.TextResult(fmt.Sprintf("kept '%s'", args.Filename)), nil
		},
		core.WithInputSchemaOverride(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type":        "string",
					"description": "File the user is being asked to confirm deletion of",
					"default":     "important.txt",
				},
			},
		}),
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))

	// multi_input: required task support. Fans out two TaskElicit calls in
	// parallel so the task surfaces TWO simultaneous inputRequests, exercising
	// the SEP-2663 partial-fulfillment path: a client may answer one key on
	// tasks/update, observe the task is still input_required with the other
	// key remaining, and answer the second on a follow-up tasks/update.
	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("multi_input",
		"Asks for two simultaneous inputs (name + confirm) so partial inputResponses can be exercised.",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			tc := tasks.GetTaskContext(ctx)
			if tc == nil {
				// SEP-2663 Option 2: TaskElicit needs the continuation
				// goroutine's TaskContext.
				return core.GoAsyncResult{}, nil
			}

			var (
				wg         sync.WaitGroup
				nameRes    core.ElicitationResult
				confirmRes core.ElicitationResult
				nameErr    error
				confirmErr error
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				nameRes, nameErr = tc.TaskElicit(core.ElicitationRequest{
					Message:         "Enter your name:",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
				})
			}()
			go func() {
				defer wg.Done()
				confirmRes, confirmErr = tc.TaskElicit(core.ElicitationRequest{
					Message:         "Confirm action?",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`),
				})
			}()
			wg.Wait()

			if nameErr != nil {
				return core.ToolResult{}, fmt.Errorf("name elicit failed: %w", nameErr)
			}
			if confirmErr != nil {
				return core.ToolResult{}, fmt.Errorf("confirm elicit failed: %w", confirmErr)
			}

			name, _ := nameRes.Content["name"].(string)
			confirmed, _ := confirmRes.Content["confirm"].(bool)
			return core.TextResult(fmt.Sprintf("name=%q confirmed=%t", name, confirmed)), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))

	// protocol_error_job: required task support. Triggers a protocol-level failure
	// by panicking. In v2, protocol errors → failed + error field.
	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("protocol_error_job",
		"A job that triggers a protocol-level error (panic). In v2: failed + error field.",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				// Run the panic inside the goroutine so the middleware's
				// recover() turns it into a failed task, not a sync 500.
				return core.GoAsyncResult{}, nil
			}
			log.Printf("[protocol_error_job] starting (will panic in 500ms)...")
			time.Sleep(500 * time.Millisecond)
			panic("simulated protocol error: server internal failure")
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))

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
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
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
				log.Printf("[external_job] custom getTask for %s", taskID)
				return core.GetTaskResultV1{}, false
			},
			GetResult: func(ctx core.MethodContext, taskID string) (core.ToolResult, bool) {
				log.Printf("[external_job] custom getResult for %s", taskID)
				return core.ToolResult{}, false
			},
		},
	})

	// test_tool_with_task: SEP-2322 MRTR + SEP-2663 task composition. The
	// handler gathers user_name via an MRTR round (input_required at the
	// tools/call level) and only then escalates to async by returning the
	// GoAsync sentinel; the continuation goroutine does the work with the
	// gathered name. The upstream tasks-mrtr-composition scenario drives this.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_task",
			Description: "MRTR elicit for user_name, then escalate to async via GoAsync. The continuation goroutine returns a greeting.",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		mrtrTaskCompositionTool,
	)

	// Register v2 tasks on the server (canonical RegisterTasks since SEP-2663
	// — v2 takes the canonical name; v1 lives at RegisterTasksV1).
	tasks.Register(tasks.Config{Server: srv})
}

// mrtrTaskCompositionTool walks two phases that meet at the GoAsync sentinel.
// Phase 1 (sync): drive the MRTR round-trip for user_name; do NOT mint a task
// until the input is in hand. Phase 2 (continuation goroutine, TaskContext
// attached): the inputResponses gathered in phase 1 are still on ctx, so the
// async work uses the name. Per SEP-2663 the MRTR requestState is not carried
// into the task's own state.
func mrtrTaskCompositionTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	// Phase 2: running inside the continuation goroutine.
	if tasks.GetTaskContext(ctx) != nil {
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

	// Phase 1: synchronous. Gather user_name via MRTR before minting a task.
	if ctx.InputResponse("user_name") == nil {
		return ctx.RequestInput(core.InputRequests{
			"user_name": core.InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
			},
		})
	}

	// MRTR loop complete; escalate to async.
	return core.GoAsyncResult{}, nil
}

// Example: HTMX MCP App — zero custom JavaScript.
//
// A task board app where the LLM adds/completes tasks. The iframe uses
// HTMX to swap partial HTML updates driven by the bridge's CustomEvent
// dispatch (mcp:toolresult). No custom JS event wiring needed.
//
// Run:  go run . -addr :8080
// Connect MCPJam to http://localhost:8080/mcp, ask "add a task to buy groceries".
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

//go:embed templates/page.html
var pageTemplateRaw string

//go:embed templates/tasks.html
var tasksTemplateRaw string

// Task is a simple task item.
type Task struct {
	Title    string `json:"title"`
	Priority string `json:"priority"`
	Done     bool   `json:"done"`
}

// In-memory task store.
var (
	tasks   []Task
	tasksMu sync.Mutex
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Parse templates.
	pageTmpl := template.Must(template.New("page").Parse(pageTemplateRaw))
	template.Must(pageTmpl.Parse(ui.BridgeTemplateDef()))
	tasksTmpl := template.Must(template.New("tasks").Parse(tasksTemplateRaw))

	// Pre-render the page with bridge.
	var pageBuf bytes.Buffer
	if err := pageTmpl.Execute(&pageBuf, struct{ Bridge ui.BridgeData }{
		Bridge: ui.NewBridgeData("task-board", "0.1.0"),
	}); err != nil {
		log.Fatal(err)
	}
	pageHTML := pageBuf.String()

	srv := server.NewServer(
		core.ServerInfo{Name: "task-board", Version: "0.1.0"},
		server.WithExtension(&ui.UIExtension{}),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)

	// Register tools.
	type addTaskInput struct {
		Title    string `json:"title"    jsonschema:"description=Task title,required"`
		Priority string `json:"priority,omitempty" jsonschema:"enum=low,enum=medium,enum=high,default=medium"`
	}
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[addTaskInput, core.ToolResult]{
		Name:        "add_task",
		Description: "Add a task to the board",
		Handler: func(ctx core.ToolContext, input addTaskInput) (core.ToolResult, error) {
			if input.Priority == "" {
				input.Priority = "medium"
			}
			tasksMu.Lock()
			tasks = append(tasks, Task{Title: input.Title, Priority: input.Priority})
			count := len(tasks)
			tasksMu.Unlock()
			return core.StructuredResult(
				"Added task: "+input.Title,
				map[string]any{"title": input.Title, "priority": input.Priority, "total": count},
			), nil
		},
		ResourceURI: "ui://tasks/board",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: pageHTML,
			}}}, nil
		},
	})

	type completeTaskInput struct {
		Title string `json:"title" jsonschema:"description=Task title to complete"`
	}
	srv.Register(core.TextTool[completeTaskInput]("complete_task", "Mark a task as done by title",
		func(ctx core.ToolContext, input completeTaskInput) (string, error) {
			tasksMu.Lock()
			found := false
			for i := range tasks {
				if tasks[i].Title == input.Title {
					tasks[i].Done = true
					found = true
				}
			}
			tasksMu.Unlock()
			if !found {
				return "Task not found: " + input.Title, nil
			}
			return "Completed: " + input.Title, nil
		},
	))

	srv.Register(core.TypedTool[struct{}, core.ToolResult]("list_tasks", "List all tasks on the board",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			tasksMu.Lock()
			data, _ := json.Marshal(tasks)
			tasksMu.Unlock()
			return core.StructuredResult("Tasks: "+string(data), map[string]any{"tasks": tasks}), nil
		},
	))

	// --- Elicitation demo: confirm priority before adding ---
	type confirmTaskInput struct {
		Title string `json:"title" jsonschema:"description=Task title,required"`
	}
	srv.Register(core.TextTool[confirmTaskInput]("add_task_confirmed",
		"Add a task with priority confirmation — asks the user to pick a priority via elicitation before adding",
		func(ctx core.ToolContext, input confirmTaskInput) (string, error) {
			result, err := ctx.Elicit(core.ElicitationRequest{
				Message: fmt.Sprintf("Choose priority for task: %q", input.Title),
				RequestedSchema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"priority": {
							"type": "string",
							"enum": ["low", "medium", "high"],
							"default": "medium",
							"description": "Task priority"
						}
					}
				}`),
			})
			if err != nil {
				return fmt.Sprintf("Elicitation failed: %v", err), nil
			}
			if result.Action != "accept" {
				return fmt.Sprintf("Task creation cancelled (action=%s)", result.Action), nil
			}
			priority, _ := result.Content["priority"].(string)
			if priority == "" {
				priority = "medium"
			}
			tasksMu.Lock()
			tasks = append(tasks, Task{Title: input.Title, Priority: priority})
			tasksMu.Unlock()
			return fmt.Sprintf("Added task %q with priority %s (confirmed by user)", input.Title, priority), nil
		},
	))

	// --- Sampling demo: auto-categorize task priority ---
	type categorizeInput struct {
		Title string `json:"title" jsonschema:"description=Task title to categorize,required"`
	}
	srv.Register(core.TextTool[categorizeInput]("categorize_task",
		"Use the LLM to suggest a priority for a task based on its title",
		func(ctx core.ToolContext, input categorizeInput) (string, error) {
			result, err := ctx.Sample(core.CreateMessageRequest{
				Messages: []core.SamplingMessage{{
					Role: "user",
					Content: core.Content{
						Type: "text",
						Text: fmt.Sprintf(
							"Given this task title, suggest exactly one priority: low, medium, or high. "+
								"Reply with ONLY the priority word, nothing else.\n\nTask: %q", input.Title),
					},
				}},
				MaxTokens: 10,
			})
			if err != nil {
				return fmt.Sprintf("Sampling failed: %v", err), nil
			}
			suggestion := strings.TrimSpace(strings.ToLower(result.Content.Text))
			// Normalize to valid priority
			switch suggestion {
			case "low", "medium", "high":
				// valid
			default:
				suggestion = "medium"
			}
			tasksMu.Lock()
			tasks = append(tasks, Task{Title: input.Title, Priority: suggestion})
			tasksMu.Unlock()
			return fmt.Sprintf("Added task %q with LLM-suggested priority: %s (model: %s)",
				input.Title, suggestion, result.Model), nil
		},
	))

	// --- Prompt demo: task summary ---
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "task_summary",
			Description: "Returns a formatted summary of all tasks on the board",
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			tasksMu.Lock()
			snapshot := make([]Task, len(tasks))
			copy(snapshot, tasks)
			tasksMu.Unlock()

			if len(snapshot) == 0 {
				return core.PromptResult{
					Description: "Task board summary",
					Messages: []core.PromptMessage{{
						Role:    "user",
						Content: core.Content{Type: "text", Text: "The task board is empty. No tasks have been added yet."},
					}},
				}, nil
			}

			var sb strings.Builder
			sb.WriteString("Here are the current tasks on the board:\n\n")
			for i, t := range snapshot {
				status := "pending"
				if t.Done {
					status = "done"
				}
				sb.WriteString(fmt.Sprintf("%d. [%s] %s (priority: %s)\n", i+1, status, t.Title, t.Priority))
			}
			sb.WriteString(fmt.Sprintf("\nTotal: %d tasks", len(snapshot)))

			return core.PromptResult{
				Description: "Task board summary",
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: sb.String()},
				}},
			}, nil
		},
	)

	// HTTP mux: MCP + HTMX partials.
	mux := http.NewServeMux()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)

	// HTMX partial endpoint — returns rendered task list HTML.
	mux.HandleFunc("/partial/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasksMu.Lock()
		data := struct{ Tasks []Task }{Tasks: tasks}
		tasksMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		tasksTmpl.Execute(w, data)
	})

	log.Printf("task-board listening on %s (MCP at /mcp)", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

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
	"html/template"
	"log"
	"net/http"
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
	)

	// Register tools.
	ui.RegisterAppTool(srv, ui.AppToolConfig{
		Name:        "add_task",
		Description: "Add a task to the board",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":    map[string]any{"type": "string", "description": "Task title"},
				"priority": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}, "default": "medium"},
			},
			"required": []string{"title"},
		},
		ResourceURI: "ui://tasks/board",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Title    string `json:"title"`
				Priority string `json:"priority"`
			}
			req.Bind(&args)
			if args.Priority == "" {
				args.Priority = "medium"
			}
			tasksMu.Lock()
			tasks = append(tasks, Task{Title: args.Title, Priority: args.Priority})
			count := len(tasks)
			tasksMu.Unlock()
			return core.StructuredResult(
				"Added task: "+args.Title,
				map[string]any{"title": args.Title, "priority": args.Priority, "total": count},
			), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: pageHTML,
			}}}, nil
		},
	})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "complete_task",
			Description: "Mark a task as done by title",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Task title to complete"},
				},
				"required": []string{"title"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Title string `json:"title"`
			}
			req.Bind(&args)
			tasksMu.Lock()
			found := false
			for i := range tasks {
				if tasks[i].Title == args.Title {
					tasks[i].Done = true
					found = true
				}
			}
			tasksMu.Unlock()
			if !found {
				return core.TextResult("Task not found: " + args.Title), nil
			}
			return core.TextResult("Completed: " + args.Title), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "list_tasks",
			Description: "List all tasks on the board",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tasksMu.Lock()
			data, _ := json.Marshal(tasks)
			tasksMu.Unlock()
			return core.StructuredResult("Tasks: "+string(data), map[string]any{"tasks": tasks}), nil
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

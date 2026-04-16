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

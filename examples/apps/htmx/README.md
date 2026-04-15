# Task Board — HTMX MCP App

A server-rendered MCP App with zero custom JavaScript. HTMX handles all UI updates via the bridge's `CustomEvent` dispatch.

## What it demonstrates

- Zero custom JS — only the bridge + HTMX library
- `hx-trigger="mcp:toolresult from:document"` listens for bridge events
- `hx-get="/partial/tasks"` fetches server-rendered HTML partials
- MCP tools + REST partials on the same Go HTTP mux
- Multiple tools: `add_task`, `complete_task`, `list_tasks`

## Setup

```bash
cd examples/apps/htmx
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "Task Board"

## Prompts to try

- "Add a task to buy groceries" — adds a task, iframe updates via HTMX swap
- "Add a high priority task to review the PR" — adds with priority badge
- "Mark buy groceries as done" — strikes through the task
- "What tasks do I have?" — lists all tasks
- "Add three tasks: laundry, cooking, cleaning" — bulk add, iframe updates after each

## Key files

| File | What |
|------|------|
| `templates/page.html` | Main page with HTMX + bridge template |
| `templates/tasks.html` | Partial template for task list (HTMX swaps this) |
| `main.go` | Go server: tools, in-memory task store, `/partial/tasks` endpoint |

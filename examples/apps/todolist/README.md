# Todo List — MCP App

A server-rendered MCP App with inline JavaScript. The initial state is rendered server-side in the resource handler. Live updates arrive via the bridge's `toolresult` event and update the DOM with inline JS. Demonstrates the full MCP protocol surface: tools, elicitation, sampling, and prompts.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `core.TextTool`, `core.ToolContext.Elicit`, `core.ToolContext.Sample`, `server.WithMiddleware`, `LoggingMiddleware` |
| Extension | `ext/ui` — `UIExtension`, `RegisterTypedAppTool`, `BridgeTemplateDef`, `NewBridgeData` |
| MCP primitives | Tools, Resources (App), Elicitation, Sampling, Prompts |

## What it demonstrates

- Server-rendered initial state via Go `html/template`
- Bridge `toolresult` event drives live DOM updates (no external scripts or fetches)
- Works within MCP App CSP constraints (`script-src 'unsafe-inline'` only)
- **Tools**: `add_task`, `complete_task`, `list_tasks`, `add_task_confirmed`, `categorize_task`
- **Elicitation**: `add_task_confirmed` pauses to ask the user for priority confirmation
- **Sampling**: `categorize_task` asks the LLM to suggest a priority
- **Prompts**: `task_summary` returns a formatted overview of all items
- **Middleware**: `LoggingMiddleware` logs every JSON-RPC request

## Screenshots

<!-- TODO: add screenshots -->
![Todo List](screenshots/todolist.png)
![Elicitation](screenshots/elicitation.png)

## Setup

```bash
cd examples/apps/todolist
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "Todo List"

## Prompts to try

- "Add a task to buy groceries" — adds an item, iframe updates via bridge event
- "Add a high priority task to review the PR" — adds with priority badge
- "Mark buy groceries as done" — strikes through the item
- "What tasks do I have?" — lists all items
- "Add three tasks: laundry, cooking, cleaning" — bulk add, iframe updates after each
- **"Add a task to call mom, but let me pick the priority"** — triggers elicitation flow
- **"Categorize the task 'deploy to production'"** — LLM suggests priority via sampling
- **Use the `task_summary` prompt** — formatted overview of all items

## MCP Features

| Feature | Tool/Prompt | Description |
|---------|------------|-------------|
| Tool (basic) | `add_task` | Add item with title + priority |
| Tool (basic) | `complete_task` | Mark item as done |
| Tool (basic) | `list_tasks` | List all items (structured output) |
| Elicitation | `add_task_confirmed` | Asks user to confirm priority before adding |
| Sampling | `categorize_task` | LLM suggests priority based on title |
| Prompt | `task_summary` | Formatted todo list overview |

## Key files

| File | What |
|------|------|
| `templates/page.html` | Main page with bridge + inline JS for live updates |
| `main.go` | Go server: tools, elicitation, sampling, prompts |

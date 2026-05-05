package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}

	demo := demokit.New("MCP List TTL (SEP-2549) — Cache-Freshness Hint on List Results").
		Dir("list-ttl").
		Description("Walks through SEP-2549, which adds an optional `ttl` (seconds) cache-freshness hint to every paginated list response (tools/list, prompts/list, resources/list, resources/templates/list). Clients use it to cache the registered surface between `notifications/list_changed` instead of re-fetching on every poll.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve, WithListTTL(60))"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # list-ttl server on :8080 with WithListTTL(60)",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
	)

	demo.Section("Three-state TTL contract",
		"The `ttl` field is optional and has three distinct wire shapes:",
		"",
		"- **absent** (`ttl` field omitted) — no server guidance; client falls back to `notifications/list_changed` or its own heuristics.",
		"- **`\"ttl\": 0`** — explicit \"do not cache\"; client SHOULD re-fetch every time the list is needed.",
		"- **`\"ttl\": <positive int>`** — fresh for N seconds; client SHOULD NOT re-fetch before TTL expires unless it receives `list_changed`.",
		"",
		"Server-side, `mcpkit.WithListTTL(seconds)` configures the value uniformly for all four endpoints. Negative values are treated as \"unset\" so the wire field is omitted.",
		"",
		"Client-side, `mcpkit/client.ListToolsPage(cursor)` and its three siblings (`ListPromptsPage`, `ListResourcesPage`, `ListResourceTemplatesPage`) return the typed result envelope so callers can read `TTL` alongside `NextCursor`. The pre-existing zero-arg `ListTools()` and the auto-paginating `Tools(ctx)` iterator drop the envelope — use the `*Page` helpers when the TTL hint matters.",
		"",
		"This demo connects to a server configured with `WithListTTL(60)` and inspects each endpoint. To see the other two states, restart the server with `--ttl 0` (do not cache) or omit `--ttl` entirely (unset).",
	)

	var c *client.Client

	demo.Step("Connect to the list-ttl server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("`client.NewClient(...)` + `Connect()`. SEP-2549 is purely a server-side concern; the client doesn't negotiate anything special.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "list-ttl-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	demo.Step("tools/list — TTL surfaces on the list response").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "{ tools: [...], ttl: 60 }").
		Note("`client.ListToolsPage(\"\")` returns the full envelope including `TTL *int`. The pointer distinguishes nil (no guidance) from `&0` (explicit \"do not cache\") — plain `int` would conflate them.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			page, err := c.ListToolsPage("")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    tools count: %d\n", len(page.Tools))
			fmt.Printf("    TTL:         %s\n", formatTTL(page.TTL))
			return
		})

	demo.Step("prompts/list / resources/list / resources/templates/list").
		Arrow("Host", "Server", "(same TTL contract on all four endpoints)").
		Note("SEP-2549 applies to every paginated list response. `WithListTTL(seconds)` configures the value uniformly — there's no per-endpoint override. Hit each endpoint and confirm they all return the configured TTL.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			prompts, err := c.ListPromptsPage("")
			if err != nil {
				fmt.Printf("    ERROR prompts: %v\n", err)
				return
			}
			fmt.Printf("    prompts/list:                  ttl=%s, count=%d\n",
				formatTTL(prompts.TTL), len(prompts.Prompts))

			resources, err := c.ListResourcesPage("")
			if err != nil {
				fmt.Printf("    ERROR resources: %v\n", err)
				return
			}
			fmt.Printf("    resources/list:                ttl=%s, count=%d\n",
				formatTTL(resources.TTL), len(resources.Resources))

			templates, err := c.ListResourceTemplatesPage("")
			if err != nil {
				fmt.Printf("    ERROR templates: %v\n", err)
				return
			}
			fmt.Printf("    resources/templates/list:      ttl=%s, count=%d\n",
				formatTTL(templates.TTL), len(templates.ResourceTemplates))
			return
		})

	demo.Step("Inspect the raw JSON-RPC envelope").
		Arrow("Host", "Server", "tools/list (raw)").
		DashedArrow("Server", "Host", "raw JSON: ttl wire-format check").
		Note("Bypass the typed helper and decode the raw response body to verify the wire shape — `\"ttl\": 60` as a JSON number, sitting alongside `\"tools\"` and (when paginated) `\"nextCursor\"`. Confirms the field is a JSON number, not stringified.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			raw, err := c.Call("tools/list", nil)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw.Raw, &m); err != nil {
				fmt.Printf("    ERROR decoding raw: %v\n", err)
				return
			}
			pretty, _ := json.MarshalIndent(m, "    ", "  ")
			fmt.Printf("    raw response:\n%s\n", string(pretty))
			return
		})

	demo.Section("Caching pattern",
		"A typical client integrates the TTL like this:",
		"",
		"```go",
		"page, err := c.ListToolsPage(\"\")",
		"if err != nil { /* ... */ }",
		"cache.Tools = page.Tools",
		"if page.TTL != nil && *page.TTL > 0 {",
		"    cache.ToolsExpiresAt = time.Now().Add(time.Duration(*page.TTL) * time.Second)",
		"}",
		"// Subsequent reads check cache.ToolsExpiresAt; on miss, re-fetch.",
		"// On notifications/list_changed, invalidate immediately regardless of TTL.",
		"```",
		"",
		"`page.TTL == nil` (absent) and `*page.TTL == 0` (do not cache) both mean \"don't cache from this response\"; the difference is whether the server gave guidance at all (clients may still cache nil-TTL responses with their own heuristics, but should re-fetch every time on `&0`).",
	)

	demo.Section("Where to look in the code",
		"- Server option: `server.WithListTTL(seconds int)` — server/server.go",
		"- Wire types: `core.ToolsListResult.TTL` / PromptsListResult / ResourcesListResult / ResourceTemplatesListResult — core/{tool,prompt,resource}.go",
		"- Client typed helpers: `client.ListToolsPage` / ListPromptsPage / ListResourcesPage / ListResourceTemplatesPage — client/iterators.go",
		"- Conformance: `conformance/list-ttl/scenarios.test.ts` (5 scenarios across 3 server processes; `make testconf-list-ttl` spawns + tears down)",
		"- SEP-2549 spec: https://github.com/modelcontextprotocol/specification/pull/2549",
	)

	if demokit.IsTUI() {
		demo.WithRenderer(tui.New())
	}

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// formatTTL renders the three-state TTL pointer in a human-readable form
// for the demo output. Mirrors the wire semantics: nil = "<absent>", &0 =
// "0 (do not cache)", &N = "N seconds".
func formatTTL(ttl *int) string {
	if ttl == nil {
		return "<absent — no server guidance>"
	}
	if *ttl == 0 {
		return "0 (do not cache)"
	}
	return fmt.Sprintf("%d seconds", *ttl)
}

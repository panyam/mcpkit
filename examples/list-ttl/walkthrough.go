package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func runDemo() {
	serverURL := common.ServerURL()
	wire := common.WireFromArgs()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("list-ttl-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("MCP List TTL (SEP-2549) — Cache Hints on List and Read Results").
		Dir("list-ttl").
		Description("Walks through SEP-2549, which adds two cache hints — `ttlMs` (integer milliseconds) and `cacheScope` (`public`/`private`) — to every paginated list response (tools/list, prompts/list, resources/list, resources/templates/list) and to resources/read. Clients use them to cache the registered surface between `notifications/list_changed` instead of re-fetching on every poll.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (just serve, WithListTTLMs(60000))"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # list-ttl server on :8080 with WithListTTLMs(60000)",
		"Terminal 2:  just demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
	)

	demo.Section("The ttlMs cache hint",
		"The `ttlMs` field is an integer-milliseconds freshness hint. Per the merged SEP-2549 spec it has two client-visible behaviors:",
		"",
		"- **absent or `\"ttlMs\": 0`** — the response is immediately stale; the client MAY re-fetch every time the list is needed. An absent field is the \"older server / not configured\" case; clients treat it the same as 0.",
		"- **`\"ttlMs\": <positive int>`** — fresh for N milliseconds from receipt; the client SHOULD NOT re-fetch before it expires unless it receives `list_changed`.",
		"",
		"Server-side, `mcpkit.WithListTTLMs(ms)` configures the value uniformly for all four list endpoints. Negative values are treated as \"unset\" so the wire field is omitted. mcpkit keeps `TTLMs` a `*int`: that lets a server emit an explicit `\"ttlMs\": 0` distinct from omitting the field, even though clients treat the two the same.",
		"",
		"Client-side, `mcpkit/client.ListToolsPage(cursor)` and its three siblings (`ListPromptsPage`, `ListResourcesPage`, `ListResourceTemplatesPage`) return the typed result envelope so callers can read `TTLMs` and `CacheScope` alongside `NextCursor`. The pre-existing zero-arg `ListTools()` and the auto-paginating `Tools(ctx)` iterator drop the envelope — use the `*Page` helpers when the cache hints matter.",
	)

	demo.Section("The cacheScope hint",
		"The `cacheScope` field controls who may serve a cached copy of a response, mirroring HTTP `Cache-Control: public` vs `private`:",
		"",
		"- **`\"public\"`** — no caller-specific data; any client, shared gateway, or caching proxy MAY store the response and serve it to any user.",
		"- **`\"private\"`** — caller-specific data; a cache MAY be reused only within the same authorization context and MUST NOT be shared across access tokens.",
		"",
		"When `cacheScope` is absent clients default to `\"public\"`, so a server whose response varies per caller MUST set `private` explicitly. Set both hints in one call with `server.WithListCacheControl(ttlMs, scope)`.",
	)

	var c *client.Client

	demo.Step("Connect to the list-ttl server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("`client.NewClient(...)` + `Connect()`. SEP-2549 is purely a server-side concern; the client doesn't negotiate anything special.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Initialize a session and capture the session id. SEP-2549 negotiates
# nothing special — a plain initialize is enough.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
echo "SID=$SID"`).Default(),
			demokit.MakeVariant("go", "go", `c := client.NewClient(serverURL+"/mcp",
    core.ClientInfo{Name: "list-ttl-host", Version: "1.0"},
)
if err := c.Connect(); err != nil { /* server not up — run: just serve */ }`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			opts := []client.ClientOption{
				client.WithTracerProvider(tp),
			}
			if opt, ok := wire.ClientOption(); ok {
				opts = append(opts, opt)
			}
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "list-ttl-host", Version: "1.0"},
				opts...,
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	demo.Step("tools/list — cache hints surface on the list response").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "{ tools: [...], ttlMs: 60000 }").
		Note("`client.ListToolsPage(\"\")` returns the full envelope including `TTLMs *int` and `CacheScope string`.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# tools/list — the cache hints ride alongside "tools" in the result.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  | jq '{ttlMs: .result.ttlMs, cacheScope: .result.cacheScope, count: (.result.tools | length)}'`).Default(),
			demokit.MakeVariant("go", "go", `// The *Page helpers return the typed envelope; the zero-arg ListTools()
// and the Tools(ctx) iterator drop the cache hints.
page, _ := c.ListToolsPage("")
_ = page.TTLMs      // *int — nil/absent and 0 both mean "immediately stale"
_ = page.CacheScope // "" defaults to "public"
_ = page.Tools`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			page, err := c.ListToolsPage("")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    tools count: %d\n", len(page.Tools))
			fmt.Printf("    ttlMs:       %s\n", formatTTLMs(page.TTLMs))
			fmt.Printf("    cacheScope:  %s\n", formatScope(page.CacheScope))
			return
		})

	demo.Step("prompts/list / resources/list / resources/templates/list").
		Arrow("Host", "Server", "(same cache-hint contract on all four list endpoints)").
		Note("SEP-2549 applies to every paginated list response. `WithListTTLMs` / `WithListCacheControl` configure the values uniformly — there's no per-endpoint override. Hit each endpoint and confirm they all return the configured hints.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Same cache-hint contract on the other three list endpoints.
for M in prompts/list resources/list resources/templates/list; do
  curl -s -X POST http://localhost:8080/mcp \
    -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$M\",\"params\":{}}" \
    | jq "{method: \"$M\", ttlMs: .result.ttlMs, cacheScope: .result.cacheScope}"
done`).Default(),
			demokit.MakeVariant("go", "go", `// Each *Page sibling carries the same TTLMs / CacheScope hints.
prompts, _ := c.ListPromptsPage("")
resources, _ := c.ListResourcesPage("")
templates, _ := c.ListResourceTemplatesPage("")
_ = prompts.TTLMs
_ = resources.CacheScope
_ = templates.TTLMs`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			prompts, err := c.ListPromptsPage("")
			if err != nil {
				fmt.Printf("    ERROR prompts: %v\n", err)
				return
			}
			fmt.Printf("    prompts/list:                  ttlMs=%s, scope=%s, count=%d\n",
				formatTTLMs(prompts.TTLMs), formatScope(prompts.CacheScope), len(prompts.Prompts))

			resources, err := c.ListResourcesPage("")
			if err != nil {
				fmt.Printf("    ERROR resources: %v\n", err)
				return
			}
			fmt.Printf("    resources/list:                ttlMs=%s, scope=%s, count=%d\n",
				formatTTLMs(resources.TTLMs), formatScope(resources.CacheScope), len(resources.Resources))

			templates, err := c.ListResourceTemplatesPage("")
			if err != nil {
				fmt.Printf("    ERROR templates: %v\n", err)
				return
			}
			fmt.Printf("    resources/templates/list:      ttlMs=%s, scope=%s, count=%d\n",
				formatTTLMs(templates.TTLMs), formatScope(templates.CacheScope), len(templates.ResourceTemplates))
			return
		})

	demo.Step("resources/read — cache hints on a read response").
		Arrow("Host", "Server", "resources/read file:///fixture").
		DashedArrow("Server", "Host", "{ contents: [...], ttlMs: 60000 }").
		Note("SEP-2549 added resources/read to the cacheable coverage mid-cycle. `client.ReadResourceFull` returns `core.ResourceResult`, which carries the same `TTLMs` / `CacheScope` fields. A read handler MAY override either per-read; otherwise the `WithReadResourceCacheControl` server default applies.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# resources/read carries the same hints (SEP-2549 added it mid-cycle).
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///fixture"}}' \
  | jq '{ttlMs: .result.ttlMs, cacheScope: .result.cacheScope, contents: (.result.contents | length)}'`).Default(),
			demokit.MakeVariant("go", "go", `// ReadResourceFull returns core.ResourceResult with the cache hints; the
// plain ReadResource drops them.
rr, _ := c.ReadResourceFull("file:///fixture")
_ = rr.TTLMs
_ = rr.CacheScope
_ = rr.Contents`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			rr, err := c.ReadResourceFull("file:///fixture")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    contents:   %d item(s)\n", len(rr.Contents))
			fmt.Printf("    ttlMs:      %s\n", formatTTLMs(rr.TTLMs))
			fmt.Printf("    cacheScope: %s\n", formatScope(rr.CacheScope))
			return
		})

	demo.Step("Inspect the raw JSON-RPC envelope").
		Arrow("Host", "Server", "tools/list (raw)").
		DashedArrow("Server", "Host", "raw JSON: ttlMs + cacheScope wire-format check").
		Note("Bypass the typed helper and decode the raw response body to verify the wire shape — `\"ttlMs\": 60000` as a JSON number and `\"cacheScope\"` as a string, sitting alongside `\"tools\"` and (when paginated) `\"nextCursor\"`.").
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
		"A typical client integrates the hints like this:",
		"",
		"```go",
		"page, err := c.ListToolsPage(\"\")",
		"if err != nil { /* ... */ }",
		"cache.Tools = page.Tools",
		"if page.TTLMs != nil && *page.TTLMs > 0 {",
		"    cache.ToolsExpiresAt = time.Now().Add(time.Duration(*page.TTLMs) * time.Millisecond)",
		"}",
		"// Subsequent reads check cache.ToolsExpiresAt; on miss, re-fetch.",
		"// On notifications/list_changed, invalidate immediately regardless of TTL.",
		"```",
		"",
		"An absent `TTLMs` and `*page.TTLMs == 0` both mean \"immediately stale — do not rely on this response being fresh\". A `private` cacheScope means the entry MUST NOT be reused across authorization contexts; key any shared cache by access token.",
	)

	demo.Section("Where to look in the code",
		"- Server options: `server.WithListTTLMs` / `WithListCacheControl` / `WithReadResourceCacheControl` — server/server.go",
		"- Wire types: `core.ToolsListResult` / PromptsListResult / ResourcesListResult / ResourceTemplatesListResult / ResourceResult — core/{tool,prompt,resource}.go; `core.CacheScopePublic` / `CacheScopePrivate` — core/cache.go",
		"- Client typed helpers: `client.ListToolsPage` / ListPromptsPage / ListResourcesPage / ListResourceTemplatesPage / ReadResource — client/iterators.go",
		"- Migration guide: docs/LIST_TTL_MIGRATION.md",
		"- Conformance: SEP-2549 scenarios on panyam/mcpconformance `pending` (`src/scenarios/server/list-ttl/`) — originally driven by a dedicated `testconf-list-ttl` suite, now folded into `just testconf`",
		"- SEP-2549 spec: https://github.com/modelcontextprotocol/specification/pull/2549",
	)

	common.SetupRenderer(demo)

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// formatTTLMs renders the TTLMs pointer in a human-readable form for the
// demo output. Mirrors the merged SEP-2549 wire semantics: nil = absent,
// &0 = "0 (immediately stale)", &N = "N ms".
func formatTTLMs(ttl *int) string {
	if ttl == nil {
		return "<absent — immediately stale>"
	}
	if *ttl == 0 {
		return "0 (immediately stale)"
	}
	return fmt.Sprintf("%d ms", *ttl)
}

// formatScope renders the cacheScope string, naming the absent-field default.
func formatScope(scope string) string {
	if scope == "" {
		return `<absent — defaults to "public">`
	}
	return scope
}

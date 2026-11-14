package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/experimental/ext/groups"
	"github.com/panyam/mcpkit/ext/skills"
)

func runDemo() {
	serverURL := common.ServerURL()
	wire := common.WireFromArgs()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("disclosure-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("Progressive disclosure: 200 tools, eight groups and two skills").
		Dir("disclosure").
		Description("A server with 200 tools sends all 200 in `tools/list`, and a client puts every schema into the model's context. This walkthrough counts those bytes on a fixture server and loads a skill. Then it connects a second client that declares the experimental groups extension. That client sees eight groups instead of 200 tools, and it fetches one group's tools and skill when the model picks it.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (just serve, 200 tools in 8 groups + 2 skills)"),
		)

	demo.Section("Setup",
		"Start the server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # 200 tools in 8 groups + 2 skills on :8080",
		"Terminal 2:  just demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"Both sides take `--wire=legacy` or `--wire=stateless`. The results are the same on either wire.",
	)

	var c *client.Client

	demo.Step("Connect").
		Arrow("Host", "Server", "initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities, including the skills extension").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			opts := []client.ClientOption{client.WithTracerProvider(tp)}
			if opt, ok := wire.ClientOption(); ok {
				opts = append(opts, opt)
			}
			c = client.NewClient(serverURL+"/mcp", core.ClientInfo{Name: "disclosure-host", Version: "0.1.0"}, opts...)
			if err := c.Connect(ctx.Ctx); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
				c = nil
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	demo.Step("tools/list returns all 200 tools").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "{ tools: [200 schemas] }").
		Note("The tools come in eight themes: billing, deploy, search, users, storage, metrics, alerts and docs. A model that needs one billing tool still receives all 200 schemas. The host sends that many bytes to the model before the model does anything.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if c == nil {
				return
			}
			n, size, err := listAllTools(ctx.Ctx, c)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    %d tools, %d bytes of JSON\n", n, size)
			fmt.Printf("    about %d bytes per theme\n", size/len(themes))
			return
		})

	var entries []skills.SkillEntry
	var sc *skills.Client

	demo.Step("skills/list names the skills without their bodies").
		Arrow("Host", "Server", "skills/list").
		DashedArrow("Server", "Host", "{ skills: [name, description, digest per file] }").
		Note("A skill is a `SKILL.md` with instructions, served under `skill://`. The listing carries each skill's name, description and file digests. The host reads a body only when the model picks that skill.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if c == nil {
				return
			}
			sc = skills.NewClient(c)
			var err error
			entries, err = sc.ListSkillEntries(ctx.Ctx)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].URI < entries[j].URI })
			for _, e := range entries {
				fmt.Printf("    %s\n", e.URI)
			}
			return
		})

	demo.Step("Read billing-refunds and verify its digest").
		Arrow("Host", "Server", "resources/read skill://billing-refunds/SKILL.md").
		DashedArrow("Server", "Host", "SKILL.md body").
		Note("`ReadFromEntry` checks the served bytes against the size and digest from the listing before the host uses them. The skill names the billing tools it needs, but it can't carry their schemas. Groups deliver those.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if sc == nil {
				return
			}
			for _, e := range entries {
				if !strings.HasSuffix(e.URI, "/billing-refunds/SKILL.md") {
					continue
				}
				res, err := sc.ReadFromEntry(ctx.Ctx, e, e.URI)
				if err != nil {
					fmt.Printf("    ERROR: %v\n", err)
					return
				}
				fmt.Printf("    %d bytes, digest verified: %v\n", len(res.Bytes), res.DigestVerified)
				return
			}
			fmt.Println("    billing-refunds not listed")
			return
		})

	demo.Section("Groups (experimental)",
		"The Progressive Disclosure working group drafts a groups extension, `io.modelcontextprotocol/groups`. The draft has no sponsor yet, and mcpkit serves it from `experimental/ext/groups`. This server puts each theme's 25 tools in a group named after the theme. The billing group also lists the billing-refunds skill.",
		"",
		"A client opts in by declaring the extension, with `groups.ClientOption()`. The plain client above declared nothing, so it keeps the flat list.",
	)

	var gc *client.Client
	var groupsClient *groups.Client
	flatBytes := 0

	demo.Step("A groups client sees groups instead of tools").
		Arrow("Host", "Server", "tools/list  (_meta declares groups)").
		DashedArrow("Server", "Host", "{ tools: [] }").
		Arrow("Host", "Server", "groups/list").
		DashedArrow("Server", "Host", "{ groups: [billing, deploy, ...] }").
		Note("Every tool on this server sits in a group, so the groups client's `tools/list` comes back empty. `groups/list` carries a name and a one-line description per group, which is what the model picks from.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			opts := []client.ClientOption{client.WithTracerProvider(tp), groups.ClientOption()}
			if opt, ok := wire.ClientOption(); ok {
				opts = append(opts, opt)
			}
			gc = client.NewClient(serverURL+"/mcp", core.ClientInfo{Name: "disclosure-groups-host", Version: "0.1.0"}, opts...)
			if err := gc.Connect(ctx.Ctx); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				gc = nil
				return
			}
			groupsClient = groups.NewClient(gc)
			n, size, err := listAllTools(ctx.Ctx, gc)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    tools/list: %d tools, %d bytes\n", n, size)
			gs, err := groupsClient.List(ctx.Ctx)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			for _, g := range gs {
				fmt.Printf("    %-8s %s\n", g.Name, g.Description)
			}
			return
		})

	demo.Step("Fetch the billing group").
		Arrow("Host", "Server", "groups/get {name: \"billing\"}").
		DashedArrow("Server", "Host", "{ instructions, tools: [25], resources: [SKILL.md] }").
		Note("The model picked billing, so the host fetches it. The result carries 25 complete tool definitions, the group's instructions, and the skill's URI in `resources`. The host still verifies the skill through the skills extension, as in the step above.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if groupsClient == nil || c == nil {
				return
			}
			g, err := groupsClient.Get(ctx.Ctx, "billing")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    instructions: %s\n", g.Instructions)
			fmt.Printf("    tools: %d, first %s\n", len(g.Tools), g.Tools[0].Name)
			for _, r := range g.Resources {
				fmt.Printf("    resource: %s\n", r.URI)
			}
			raw, err := gc.Call(ctx.Ctx, groups.MethodGet, map[string]any{"name": "billing"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if flatBytes == 0 {
				_, flatBytes, _ = listAllTools(ctx.Ctx, c)
			}
			fmt.Printf("    groups/get billing: %d bytes, flat tools/list: %d bytes\n", len(raw.Raw), flatBytes)
			return
		})

	demo.Section("Where to look in the code",
		"- The fixture: `fixture.go` in this directory",
		"- Skills provider and client: `ext/skills/provider.go`, `ext/skills/client_methods.go`",
		"- Groups catalog, list filter and client: `experimental/ext/groups/catalog.go`, `experimental/ext/groups/client.go`",
		"- The seam that lets an extension hide primitives per caller: `server/list_scope.go`",
		"- The checks `make exercise-disclosure` runs: `check.go` in this directory",
	)

	common.SetupRenderer(demo)
	demo.Execute()

	if c != nil {
		c.Close()
	}
	if gc != nil {
		gc.Close()
	}
}

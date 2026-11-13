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

	demo := demokit.New("Progressive disclosure: 200 tools and two skills").
		Dir("disclosure").
		Description("A server with 200 tools sends all 200 in `tools/list`, and a client puts every schema into the model's context. This walkthrough counts those bytes on a fixture server, then loads a skill, which a client reads only when the model needs it.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (just serve, 200 tools + 2 skills)"),
		)

	demo.Section("Setup",
		"Start the server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # 200 tools + 2 skills on :8080",
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
		Note("`ReadFromEntry` checks the served bytes against the size and digest from the listing before the host uses them. The skill names the billing tools it needs. A groups-aware server makes that link explicit.").
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

	demo.Section("Where groups fit",
		"The Progressive Disclosure working group drafts a groups extension, `io.modelcontextprotocol/groups`. A server that serves it shows one name and description per theme in `groups/list`. The client calls `groups/get` when the model picks a theme, and only then sees that theme's tools and skills. mcpkit does not serve groups yet. A groups server would need themes and skill links, and this fixture already has both.",
	)

	demo.Section("Where to look in the code",
		"- The fixture: `fixture.go` in this directory",
		"- Skills provider and client: `ext/skills/provider.go`, `ext/skills/client_methods.go`",
		"- The checks `make exercise-disclosure` runs: `check.go` in this directory",
	)

	common.SetupRenderer(demo)
	demo.Execute()

	if c != nil {
		c.Close()
	}
}

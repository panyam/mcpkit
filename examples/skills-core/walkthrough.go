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
	"github.com/panyam/mcpkit/ext/skills"
)

const uriCommitHelper = "skill://commit-helper/SKILL.md"

func runDemo() {
	serverURL := common.ServerURL()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("skills-core-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("MCP Skills — Minimal Shape (SEP-2640, scoped down)").
		Dir("skills").
		Description("The minimal SEP-2640 shape the WG blessed on 2026-06-30: a skills file served over MCP's Resources primitive plus tool handling, consumed load-on-demand. No archives, no remote sources, no fsnotify — those are the deferred / extended surface (see examples/skills for the full walkthrough).").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup",
		"```",
		"Terminal 1:  make serve   # skills-core server, file mode, :8080",
		"Terminal 2:  make demo    # this walkthrough (--tui interactive)",
		"```",
	)

	demo.Section("What 'minimal' means",
		"Serve each file under a skill directory as a `skill://` URI, enumerate them at `skill://index.json` with a SHA-256 digest, and let a skill's instructions point the host at a real tool. That is the whole blessed core: **skills file + tool handling**, consumed load-on-demand. Archives, remote sources, and fsnotify invalidation are deferred/extended and live in `examples/skills`.",
	)

	var (
		c        *client.Client
		wireMode = client.ClientModeAdaptive
	)

	demo.Step("Choose the client wire mode").
		Input(demokit.Choice("adaptive", "stateless", "legacy").
			Named("wire", "Wire mode (adaptive probes stateless first, falls back to legacy)").
			WithDefault("adaptive")).
		Note("mcpkit's server defaults to dual-wire (SEP-2575): one URL answers both the legacy initialize handshake and the server/discover probe. The rest of the walkthrough is identical either way.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			switch ctx.Inputs["wire"].(string) {
			case "stateless":
				wireMode = client.ClientModeStateless
			case "legacy":
				wireMode = client.ClientModeLegacyOnly
			default:
				wireMode = client.ClientModeAdaptive
			}
			fmt.Printf("    Selected: client.WithClientMode(%s)\n", wireMode)
			return nil
		})

	demo.Step("Connect to the skills server").
		Arrow("Host", "Server", "server/discover (stateless) OR initialize (legacy)").
		DashedArrow("Server", "Host", "serverInfo + capabilities.extensions.skills").
		VerbatimVariants("Reproduce",
			demokit.MakeVariant("go", "go", `c := client.NewClient(serverURL+"/mcp",
    core.ClientInfo{Name: "skills-core-host", Version: "1.0"},
    client.WithClientMode(wireMode),
)
if err := c.Connect(); err != nil { /* run: make serve */ }`).Default(),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "skills-core-host", Version: "1.0"},
				client.WithTracerProvider(tp),
				client.WithClientMode(wireMode),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s (wire: %s)\n",
				c.ServerInfo.Name, c.ServerInfo.Version, wireLabel(c.UsingStatelessWire()))
			if c.ServerSupportsExtension(skills.ExtensionID) {
				fmt.Printf("    Server advertises %q under capabilities.extensions\n", skills.ExtensionID)
			} else {
				fmt.Printf("    WARNING: server did NOT advertise the skills capability\n")
			}
			return nil
		})

	demo.Step("resources/list — each skill file is a skill:// resource").
		Arrow("Host", "Server", "resources/list").
		DashedArrow("Server", "Host", "[ skill://index.json, skill://commit-helper/SKILL.md ]").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			defs, err := c.ListResources(ctx.Ctx)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    %d resources:\n", len(defs))
			for _, d := range defs {
				fmt.Printf("      %s    [%s]\n", d.URI, d.MimeType)
			}
			return nil
		})

	demo.Step("Read skill://index.json — the discovery catalog").
		Arrow("Host", "Server", "resources/read uri=skill://index.json").
		DashedArrow("Server", "Host", "{ $schema, skills:[{name,url,digest}] }").
		Note("mcpkit generates index.json from the live provider catalog on each cache miss — it is not a file on disk. Each entry carries a SHA-256 digest over the skill's SKILL.md.").
		VerbatimVariants("Reproduce",
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource(skills.IndexURI)
var idx skills.Index
json.Unmarshal([]byte(body), &idx)
for _, e := range idx.Skills {
    fmt.Printf("%s digest=%s\n", e.Name, e.Digest)
}`).Default(),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(skills.IndexURI)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			var idx skills.Index
			if err := json.Unmarshal([]byte(body), &idx); err != nil {
				fmt.Printf("    ERROR: index does not parse: %v\n", err)
				return nil
			}
			fmt.Printf("    $schema: %s\n", idx.Schema)
			for _, e := range idx.Skills {
				digest := e.Digest
				if len(digest) > 14 {
					digest = digest[:14] + "…"
				}
				fmt.Printf("      %s  digest=%s\n        url=%s\n", e.Name, digest, e.URL)
			}
			return nil
		})

	demo.Step("Read the commit-helper SKILL.md — the skill file").
		Arrow("Host", "Server", "resources/read uri="+uriCommitHelper).
		DashedArrow("Server", "Host", "frontmatter (name, description) + body").
		Note("A skill is data: markdown with YAML frontmatter. Its body tells the host which tool to call and how. mcpkit delivers it over resources/read — never staged to disk, never executed.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriCommitHelper)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fm, _, err := skills.ParseFrontmatter([]byte(body))
			if err != nil {
				fmt.Printf("    ERROR parsing frontmatter: %v\n", err)
				return nil
			}
			fmt.Printf("    name:        %s\n", fm.Name)
			fmt.Printf("    description: %s\n", fm.Description)
			return nil
		})

	demo.Step("Tool handling — call the tool the skill points at").
		Arrow("Host", "Server", `tools/call name=format_commit {type:"feat", scope:"skills", summary:"..."}`).
		DashedArrow("Server", "Host", "feat(skills): ...").
		Note("This is the 'tool handling' half of the minimal shape. The skill guided the host to format_commit; the host calls it and returns the result. Skills make ordinary tools easier to use well — the pattern Paul Withers raised in-channel on 2026-07-01.").
		VerbatimVariants("Reproduce",
			demokit.MakeVariant("go", "go", `out, _ := c.ToolCall("format_commit", map[string]any{
    "type": "feat", "scope": "skills", "summary": "add commit-helper skill",
})`).Default(),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			out, err := c.ToolCall("format_commit", map[string]any{
				"type":    "feat",
				"scope":   "skills",
				"summary": "add commit-helper skill",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    format_commit → %s\n", out)
			return nil
		})

	demo.Execute()
}

func wireLabel(stateless bool) string {
	if stateless {
		return "stateless (SEP-2575)"
	}
	return "legacy"
}

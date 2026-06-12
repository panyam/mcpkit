package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/ext/skills"
)

const (
	uriGitWorkflow    = "skill://git-workflow/SKILL.md"
	uriPDFManifest    = "skill://pdf-processing/SKILL.md"
	uriPDFRef         = "skill://pdf-processing/references/FORMS.md"
	uriRefundsManifest = "skill://acme/billing/refunds/SKILL.md"
	uriRefundsEmail    = "skill://acme/billing/refunds/templates/email.md"
	uriIndex           = skills.IndexURI
)

func runDemo() {
	serverURL := common.ServerURL()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("skills-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("MCP Skills Extension (SEP-2640) — Reference Walkthrough").
		Dir("skills").
		Description("SEP-2640 serves Agent Skills over MCP's Resources primitive: each file under a skill directory is a `skill://` URI; `skill://index.json` enumerates them with SHA-256 digests.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve, file mode by default)"),
		)

	demo.Section("Setup",
		"```",
		"Terminal 1:  make serve         # default (file mode, :8080)",
		"             make serve-archive # one .tar.gz per skill",
		"Terminal 2:  make demo          # this walkthrough (--tui interactive)",
		"```",
	)

	demo.Section("URI shape",
		"`skill://<path>/<file>`. Final path segment = the skill's frontmatter `name`. Prefix segments (e.g. `acme/billing/`) are an optional server-chosen namespace. Walkthrough exercises `git-workflow`, `pdf-processing`, and `acme/billing/refunds`.",
	)

	demo.Section("Capability declaration",
		"Server advertises `io.modelcontextprotocol/skills` under `capabilities.extensions` (always `{}`, never an array). `Provider.RegisterWith(srv)` wires it automatically.",
	)

	var (
		c          *client.Client
		serverInfo any
	)

	demo.Step("Connect to the skills server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities (with extensions.skills = {})").
		Note("`client.NewClient(...)` + `Connect()`. The server side wired the extension automatically via `Provider.RegisterWith`.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "skills-host", Version: "1.0"},
				client.WithTracerProvider(tp),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			serverInfo = c.ServerInfo
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			if c.ServerSupportsExtension(skills.ExtensionID) {
				fmt.Printf("    Server advertises %q under capabilities.extensions\n", skills.ExtensionID)
			} else {
				fmt.Printf("    WARNING: server did NOT advertise the skills capability\n")
			}
			return nil
		})

	demo.Step("resources/list returns every cataloged skill URI").
		Arrow("Host", "Server", "resources/list").
		DashedArrow("Server", "Host", "resources[] including skill://index.json + each SKILL.md").
		Note("In file mode the list has N entries per skill (one for SKILL.md, one for each supporting file) plus the index. In archive mode it's one entry per skill plus the index.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			defs, err := c.ListResources()
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("    %d resources registered:\n", len(defs))
			for _, d := range defs {
				fmt.Printf("      %s    [%s]\n", d.URI, d.MimeType)
			}
			return nil
		})

	demo.Section("Discovery index",
		"`skill://index.json` enumerates skills with `{$schema, skills:[{type, name, description, url, digest}]}`. Optional in the SEP; mcpkit auto-registers unless `WithoutIndex()`.",
	)

	var indexBody []byte

	demo.Step("Read skill://index.json").
		Arrow("Host", "Server", "resources/read uri=skill://index.json").
		DashedArrow("Server", "Host", "{ $schema, skills: [...] }").
		Note("The Indexer caches the result with TTL + per-skill mtime invalidation. Repeated reads return the same bytes until something in a SKILL.md actually changes.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriIndex)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			indexBody = []byte(body)
			var idx skills.Index
			if err := json.Unmarshal(indexBody, &idx); err != nil {
				fmt.Printf("    ERROR: index does not parse: %v\n", err)
				return nil
			}
			fmt.Printf("    $schema: %s\n", idx.Schema)
			fmt.Printf("    %d entries:\n", len(idx.Skills))
			for _, e := range idx.Skills {
				suffix := ""
				if e.Digest != "" {
					suffix = " digest=" + e.Digest[:14] + "…"
				}
				fmt.Printf("      [%s] %s%s\n        url=%s\n", e.Type, e.Name, suffix, e.URL)
			}
			return nil
		})

	demo.Section("Digest contract",
		"Each entry carries `sha256:{64hex}` over the raw artifact bytes (SKILL.md for skill-md, packed archive for archive). Hosts MUST verify before use.",
	)

	demo.Step("Verify digest by re-fetching SKILL.md and recomputing SHA-256").
		Arrow("Host", "Server", "resources/read uri=skill://git-workflow/SKILL.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Treat the response bytes as the artifact, hash them, and compare against the digest field from the index. A mismatch indicates corruption or tampering and per the SEP the host MUST NOT use the content.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil || len(indexBody) == 0 {
				return nil
			}
			var idx skills.Index
			if err := json.Unmarshal(indexBody, &idx); err != nil {
				return nil
			}
			var want string
			for _, e := range idx.Skills {
				if e.URL == uriGitWorkflow {
					want = e.Digest
					break
				}
			}
			if want == "" {
				fmt.Printf("    git-workflow not found in index (server may be in archive mode)\n")
				return nil
			}
			result, err := c.ReadResourceFull(uriGitWorkflow)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			content := result.Contents[0]
			var raw []byte
			if content.Text != "" {
				raw = []byte(content.Text)
			} else {
				raw, _ = base64.StdEncoding.DecodeString(content.Blob)
			}
			sum := sha256.Sum256(raw)
			got := "sha256:" + hex.EncodeToString(sum[:])
			fmt.Printf("    want %s\n", want)
			fmt.Printf("    got  %s\n", got)
			if got == want {
				fmt.Printf("    digest matches — content verified per SEP-2640 §Integrity\n")
			} else {
				fmt.Printf("    DIGEST MISMATCH — content MUST NOT be used per the SEP\n")
			}
			return nil
		})

	demo.Section("Reading skill files",
		"Manifest body may reference supporting files via relative paths. `skills.ResolveRelative(skillRoot, ref)` resolves them filesystem-style; `..` escapes are rejected.",
	)

	demo.Step("Read the pdf-processing SKILL.md").
		Arrow("Host", "Server", "resources/read uri=skill://pdf-processing/SKILL.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("This skill's frontmatter has `version` and `tags` Extra fields. mcpkit surfaces those under `ResourceDef.Annotations` keyed by `io.modelcontextprotocol.skills/`.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriPDFManifest)
			if err != nil {
				fmt.Printf("    SKIP: %v (server may be in archive mode)\n", err)
				return nil
			}
			previewBody(body, 6)
			return nil
		})

	demo.Step("Read a supporting file via skill:// (references/FORMS.md)").
		Arrow("Host", "Server", "resources/read uri=skill://pdf-processing/references/FORMS.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Relative reference `references/FORMS.md` from within pdf-processing/SKILL.md resolves to this full URI via `skills.ResolveRelative(skillRoot, \"references/FORMS.md\")`.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriPDFRef)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body, 6)
			return nil
		})

	demo.Step("Read a nested-prefix skill (acme/billing/refunds)").
		Arrow("Host", "Server", "resources/read uri=skill://acme/billing/refunds/SKILL.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Demonstrates that the prefix-segment routing works end-to-end. The skill's `name` is `refunds`; the prefix `acme/billing/` is server-chosen.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriRefundsManifest)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body, 6)
			return nil
		})

	demo.Step("Read a supporting file in the nested skill (templates/email.md)").
		Arrow("Host", "Server", "resources/read uri=skill://acme/billing/refunds/templates/email.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Same relative-reference resolution: `templates/email.md` from refunds/SKILL.md.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriRefundsEmail)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body, 6)
			return nil
		})

	demo.Section("SEP-414 P7 — Skills observability",
		"Fetch ≠ activation. Server `resources/read` spans now carry `mcp.skill.*` attrs (#748). Client `ext/skills.Client` emits `skills.read*` spans + `Activate(ctx, uri)` for post-cache use the wire can't see (SDK-only — no spec change).",
	)

	demo.Step("Wrap reads in skills.NewClient(...) and call Client.Activate").
		Arrow("Host", "Server", "resources/read via sc.ReadAndVerify (span: skills.read_and_verify)").
		DashedArrow("Server", "Host", "bytes + digest match").
		Note("Activate is intra-process — no wire traffic. Run with `make serve EXPORTER=stdout` + `make demo EXPORTER=stdout` to see spans.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			activations := 0
			sc := skills.NewClient(c,
				skills.WithTracerProvider(tp),
				skills.WithActivationHook(func(_ context.Context, ev skills.ActivationEvent) {
					activations++
					fmt.Printf("    activation hook: uri=%s reason=%q ts=%s\n",
						ev.URI, ev.Reason, ev.Timestamp.Format("15:04:05.000"))
				}),
			)
			if _, err := sc.ReadAndVerify(context.Background(), uriPDFManifest, ""); err != nil {
				fmt.Printf("    SKIP ReadAndVerify: %v (server may be in archive mode)\n", err)
				return nil
			}
			fmt.Printf("    sc.ReadAndVerify emitted skills.read_and_verify span\n")
			ev := sc.Activate(context.Background(), uriPDFManifest,
				skills.WithReason("walkthrough_demonstration"))
			fmt.Printf("    sc.Activate returned ActivationEvent{URI=%s, Reason=%q}\n", ev.URI, ev.Reason)
			fmt.Printf("    %d hook invocation(s)\n", activations)
			return nil
		})

	demo.Section("Wrap-up",
		"Negotiated extension, enumerated index, verified one digest, read manifest + supporting files across single-segment and nested-prefix paths, emitted skill-shape spans + an activation event. `make serve-archive` flips the wire to one `.tar.gz` per skill — host code unchanged.",
	)

	_ = serverInfo
	common.SetupRenderer(demo)
	demo.Execute()
}

// previewBody prints up to n lines of the body for the walkthrough output.
// Skill bodies are short so this is enough to confirm the round trip without
// flooding the terminal.
func previewBody(body string, n int) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i >= n {
			fmt.Printf("    … (%d more lines)\n", len(lines)-n)
			return
		}
		fmt.Printf("    %s\n", line)
	}
}

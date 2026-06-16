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
	uriGitWorkflow         = "skill://git-workflow/SKILL.md"
	uriPDFManifest         = "skill://pdf-processing/SKILL.md"
	uriPDFRef              = "skill://pdf-processing/references/FORMS.md"
	uriRefundsManifest     = "skill://acme/billing/refunds/SKILL.md"
	uriRefundsEmail        = "skill://acme/billing/refunds/templates/email.md"
	uriRefundsTemplatesDir = "skill://acme/billing/refunds/templates"
	uriIndex               = skills.IndexURI
)

// previewHeadTailLines bounds the head and the tail of every previewBody
// output independently — the function prints up to this many lines from
// the start of the body, an elision marker, and up to this many from the
// end. Bodies of (2*previewHeadTailLines) or fewer lines print whole.
// Tune here if the audience needs more or less context per read.
const previewHeadTailLines = 10

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
		wireMode   = client.ClientModeAdaptive
	)

	demo.Section("Wire mode (SEP-2575 dual-wire)",
		"mcpkit's server defaults to `ModeDual` — every URL serves both the legacy `initialize` handshake and the SEP-2575 `server/discover` probe. Pick which wire the client should use; the rest of the walkthrough works identically either way.",
	)

	demo.Step("Choose the client wire mode").
		Input(demokit.Choice("adaptive", "stateless", "legacy").
			Named("wire", "Wire mode (adaptive probes stateless first, falls back to legacy)").
			WithDefault("adaptive")).
		Note("Adaptive (default) probes server/discover and falls back to the initialize handshake on -32601. Stateless forces server/discover and errors if the server cannot answer. Legacy skips the probe and goes straight to initialize.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			choice, _ := ctx.Inputs["wire"].(string)
			switch choice {
			case "stateless":
				wireMode = client.ClientModeStateless
			case "legacy":
				wireMode = client.ClientModeLegacyOnly
			default:
				wireMode = client.ClientModeAdaptive
			}
			fmt.Printf("    Selected: %s — client.WithClientMode(%s)\n", choice, wireMode)
			return nil
		})

	demo.Step("Connect to the skills server").
		Arrow("Host", "Server", "POST /mcp — server/discover (stateless) OR initialize (legacy)").
		DashedArrow("Server", "Host", "serverInfo + capabilities (with extensions.skills)").
		Note("Construct the client with the chosen wire mode, then connect. After the call returns, inspect the new accessor to see which wire engaged. The curl chain below uses the legacy wire and mints a session id reused by every subsequent step; the stateless wire skips that — each call posts directly to /mcp with no Mcp-Session-Id header.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Legacy wire: initialize handshake mints the session id for downstream steps.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"skills-host","version":"1.0"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
echo "SID=$SID"

# Stateless wire alternative — no session id, just probe server/discover:
#   curl -s -X POST http://localhost:8080/mcp \
#     -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
#     -d '{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `c := client.NewClient(serverURL+"/mcp",
    core.ClientInfo{Name: "skills-host", Version: "1.0"},
    client.WithClientMode(wireMode), // adaptive | stateless | legacy
)
if err := c.Connect(); err != nil { /* server not up — run: make serve */ }
stateless := c.UsingStatelessWire()
supports  := c.ServerSupportsExtension(skills.ExtensionID)`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "skills-host", Version: "1.0"},
				client.WithTracerProvider(tp),
				client.WithClientMode(wireMode),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			serverInfo = c.ServerInfo
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			fmt.Printf("    Wire: %s\n", wireLabel(c.UsingStatelessWire()))
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
		Note("In file mode the list has N entries per skill (one for SKILL.md, one per supporting file) plus the index. In archive mode it's one entry per skill plus the index.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"resources/list"}' | jq '.result.resources[] | "\(.uri)  [\(.mimeType)]"'`).Default(),
			demokit.MakeVariant("go", "go", `defs, err := c.ListResources()
for _, d := range defs {
    fmt.Printf("%s    [%s]\n", d.URI, d.MimeType)
}`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			defs, err := c.ListResources(ctx.Ctx)
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
		Note("The Indexer caches the result with a TTL and per-skill mtime invalidation. Repeated reads return the same bytes until something in a SKILL.md actually changes. The file is not on disk — mcpkit generates it from the live provider catalog on each cache miss.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' | jq '.'`).Default(),
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource(skills.IndexURI)
var idx skills.Index
json.Unmarshal([]byte(body), &idx)
for _, e := range idx.Skills {
    fmt.Printf("[%s] %s digest=%s…\n", e.Type, e.Name, e.Digest[:14])
}`),
		).
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
		Note("Treat the response bytes as the artifact, hash them, compare against the digest field from the index. A mismatch indicates corruption or tampering, and per the SEP the host MUST NOT use the content.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Re-read the SKILL.md, recompute sha256, compare against the index entry's digest.
WANT=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '.skills[] | select(.url=="skill://git-workflow/SKILL.md") | .digest')
GOT="sha256:$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"skill://git-workflow/SKILL.md"}}' \
  | jq -r '.result.contents[0].text' | shasum -a 256 | awk '{print $1}')"
[ "$WANT" = "$GOT" ] && echo "verified" || echo "MISMATCH"`).Default(),
			demokit.MakeVariant("go", "go", `result, _ := c.ReadResourceFull("skill://git-workflow/SKILL.md")
raw := []byte(result.Contents[0].Text)
sum := sha256.Sum256(raw)
got := "sha256:" + hex.EncodeToString(sum[:])
// compare got against e.Digest from skill://index.json — host MUST NOT use mismatched content`),
		).
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
		Note("This skill's frontmatter carries version and tags Extra fields. mcpkit surfaces those under ResourceDef.Annotations keyed by the io.modelcontextprotocol.skills/ reverse-domain prefix.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"skill://pdf-processing/SKILL.md"}}' \
  | jq -r '.result.contents[0].text'`).Default(),
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource("skill://pdf-processing/SKILL.md")
fmt.Println(body)`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriPDFManifest)
			if err != nil {
				fmt.Printf("    SKIP: %v (server may be in archive mode)\n", err)
				return nil
			}
			previewBody(body)
			return nil
		})

	demo.Step("Read a supporting file via skill:// (references/FORMS.md)").
		Arrow("Host", "Server", "resources/read uri=skill://pdf-processing/references/FORMS.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Relative reference resolution: references/FORMS.md from inside pdf-processing/SKILL.md resolves to this full URI via the SDK helper that walks the skill root.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"skill://pdf-processing/references/FORMS.md"}}' \
  | jq -r '.result.contents[0].text'`).Default(),
			demokit.MakeVariant("go", "go", `root, _ := skills.ParseURI("skill://pdf-processing/SKILL.md")
target, _ := skills.ResolveRelative(root, "references/FORMS.md")
body, _ := c.ReadResource(target.String())`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriPDFRef)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body)
			return nil
		})

	demo.Step("Read a nested-prefix skill (acme/billing/refunds)").
		Arrow("Host", "Server", "resources/read uri=skill://acme/billing/refunds/SKILL.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Demonstrates that the prefix-segment routing works end-to-end. The skill name is refunds; the acme/billing/ prefix is server-chosen and is opaque to the skill's own frontmatter.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"skill://acme/billing/refunds/SKILL.md"}}' \
  | jq -r '.result.contents[0].text'`).Default(),
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource("skill://acme/billing/refunds/SKILL.md")`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriRefundsManifest)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body)
			return nil
		})

	demo.Step("Read a supporting file in the nested skill (templates/email.md)").
		Arrow("Host", "Server", "resources/read uri=skill://acme/billing/refunds/templates/email.md").
		DashedArrow("Server", "Host", "text/markdown body").
		Note("Same relative-reference resolution as the pdf-processing example, this time across a multi-segment prefix.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":8,"method":"resources/read","params":{"uri":"skill://acme/billing/refunds/templates/email.md"}}' \
  | jq -r '.result.contents[0].text'`).Default(),
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource("skill://acme/billing/refunds/templates/email.md")`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource(uriRefundsEmail)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			previewBody(body)
			return nil
		})

	demo.Section("SEP-2640 directoryRead — scoped subtree navigation",
		"SEP commit `2e04c48d` (2026-06-09) added `resources/directory/read` for listing a directory's direct children without enumerating the server's entire resource space. Capability-gated via `io.modelcontextprotocol/skills.directoryRead`. mcpkit's Provider auto-supports it (#781).",
	)

	demo.Step("List a directory inside a skill and recurse into a subdirectory").
		Arrow("Host", "Server", "resources/directory/read uri=skill://acme/billing/refunds/templates").
		DashedArrow("Server", "Host", "2 files + 1 subdirectory (`regional`, inode/directory)").
		Arrow("Host", "Server", "resources/directory/read uri=skill://acme/billing/refunds/templates/regional").
		DashedArrow("Server", "Host", "1 file (eu.md)").
		Note("Subdirectories surface with mimeType inode/directory; the client descends by issuing a second call. The SDK wraps this into a single call; the curl below shows both round trips explicitly.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Level 1: list the templates/ subtree of the refunds skill.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":9,"method":"resources/directory/read","params":{"uri":"skill://acme/billing/refunds/templates"}}' \
  | jq '.result.resources[] | {uri, mimeType}'

# Level 2: descend into the regional/ subdirectory the first call returned.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":10,"method":"resources/directory/read","params":{"uri":"skill://acme/billing/refunds/templates/regional"}}' \
  | jq '.result.resources[] | {uri, mimeType}'`).Default(),
			demokit.MakeVariant("go", "go", `sc := skills.NewClient(c, skills.WithTracerProvider(tp))
result, _ := sc.ReadDirectory(ctx, "skill://acme/billing/refunds/templates")
for _, r := range result.Resources {
    if r.MimeType == skills.MimeTypeDirectory {
        sub, _ := sc.ReadDirectory(ctx, r.URI)
        // recurse over sub.Resources
        _ = sub
    }
}`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			sc := skills.NewClient(c, skills.WithTracerProvider(tp))
			if !sc.SupportsDirectoryRead() {
				fmt.Printf("    SKIP: server does not advertise directoryRead\n")
				return nil
			}
			// First listing: the templates/ subtree.
			result, err := sc.ReadDirectory(context.Background(), uriRefundsTemplatesDir)
			if err != nil {
				fmt.Printf("    SKIP: %v\n", err)
				return nil
			}
			fmt.Printf("    ReadDirectory(%s):\n", uriRefundsTemplatesDir)
			for _, r := range result.Resources {
				fmt.Printf("      %-12s %s\n", marker(r.MimeType), r.URI)
			}
			// Hand-rolled recursion: descend into any subdirectory we see.
			for _, r := range result.Resources {
				if r.MimeType != skills.MimeTypeDirectory {
					continue
				}
				sub, err := sc.ReadDirectory(context.Background(), r.URI)
				if err != nil {
					fmt.Printf("    SKIP %s: %v\n", r.URI, err)
					continue
				}
				fmt.Printf("    ReadDirectory(%s):\n", r.URI)
				for _, e := range sub.Resources {
					fmt.Printf("      %-12s %s\n", marker(e.MimeType), e.URI)
				}
			}
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

// wireLabel returns a human-readable wire-mode name for the walkthrough
// output. Picks between the SEP-2575 stateless wire (server/discover at
// connect, response-as-SSE for notifications) and the legacy wire
// (initialize handshake, SSE notification stream).
func wireLabel(stateless bool) string {
	if stateless {
		return "SEP-2575 stateless (server/discover)"
	}
	return "legacy (initialize handshake)"
}

// marker returns a one-glyph indicator for a directory listing entry based
// on its MIME type. "dir/" for SEP-2640 inode/directory entries, "file" for
// everything else. Aligns the walkthrough table without depending on a
// terminal capability lib.
func marker(mimeType string) string {
	if mimeType == skills.MimeTypeDirectory {
		return "dir/"
	}
	return "file"
}

// previewBody prints the body of a read with head-and-tail bracketing
// governed by previewHeadTailLines. Head-and-tail reads better than a
// single window because the audience sees both the frontmatter shape
// and the body's closing structure — useful when SKILL.md instructions
// reference both a top-of-file metadata block and a bottom-of-file
// closing section.
func previewBody(body string) {
	lines := strings.Split(body, "\n")
	if len(lines) <= 2*previewHeadTailLines {
		for _, line := range lines {
			fmt.Printf("    %s\n", line)
		}
		return
	}
	for _, line := range lines[:previewHeadTailLines] {
		fmt.Printf("    %s\n", line)
	}
	fmt.Printf("    … (%d more lines)\n", len(lines)-2*previewHeadTailLines)
	for _, line := range lines[len(lines)-previewHeadTailLines:] {
		fmt.Printf("    %s\n", line)
	}
}

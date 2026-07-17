package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

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
			demokit.Actor("Server", "MCP Server (just serve, file mode by default)"),
		)

	demo.Section("Setup",
		"```",
		"Terminal 1:  just serve         # default (file mode, :8080)",
		"             just serve-archive # one .tar.gz per skill",
		"             just serve-zip     # one .zip per skill",
		"Terminal 2:  just demo          # this walkthrough (--tui interactive)",
		"```",
		"This walkthrough auto-detects which of the three distribution modes the server is serving. The mode-aware section near the bottom shows the archive read-and-unpack flow; the file-mode read steps in the middle SKIP cleanly when archive mode is in effect (and vice versa).",
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
if err := c.Connect(); err != nil { /* server not up — run: just serve */ }
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
				fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
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

	var (
		indexBody []byte
		detected  modeInfo
	)

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

	demo.Section("Distribution mode",
		"SEP-2640 lets a server publish each skill as either individual files (`type:skill-md`) or one packed archive per skill (`type:archive` with `.tar.gz` / `.zip` suffix). The shape is visible on the index entries — the walkthrough sniffs it once and threads the result through the rest of the steps so the file-mode and archive-mode narratives stay tidy.",
	)

	demo.Step("Detect server distribution mode from the index").
		Note("In file mode every entry's type is skill-md; the host fetches SKILL.md plus any supporting files individually. In archive mode every entry's type is archive and the URL ends in .tar.gz or .zip; the host fetches one resource per skill and unpacks it in-process. The current Provider is per-mode (no mixing), so the first archive entry sighted in the index is enough to decide.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("jq", "bash", `# Distinct types appearing in the index — "skill-md" or "archive".
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":11,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '[.skills[].type] | unique | join(",")'`).Default(),
			demokit.MakeVariant("go", "go", `var idx skills.Index
json.Unmarshal(indexBody, &idx)
for _, e := range idx.Skills {
    if e.Type == skills.SkillTypeArchive {
        f := skills.DetectArchiveFormat(e.URL, nil) // .tar.gz or .zip
        // archive mode — fetch one resource per skill, unpack in-memory
        _ = f
        break
    }
}`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil || len(indexBody) == 0 {
				return nil
			}
			var idx skills.Index
			if err := json.Unmarshal(indexBody, &idx); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			detected = detectMode(idx)
			if detected.archive {
				fmt.Printf("    Detected mode: archive (%s)\n", detected.suffix)
				fmt.Printf("    Each skill is one packed resource — see the Archive mode section below for read + verify + unpack.\n")
			} else {
				fmt.Printf("    Detected mode: file\n")
				fmt.Printf("    Each skill is a tree of individual resources — the per-file reads below exercise that path.\n")
			}
			return nil
		})

	demo.Section("Digest contract",
		"Each entry carries `sha256:{64hex}` over the raw artifact bytes (SKILL.md for skill-md, packed archive for archive). Hosts MUST verify before use.",
	)

	demo.Step("Verify digest by re-fetching git-workflow's canonical artifact").
		Arrow("Host", "Server", "resources/read uri=skill://git-workflow{/SKILL.md | .tar.gz | .zip}").
		DashedArrow("Server", "Host", "text/markdown body (file mode) OR archive bytes (archive mode)").
		Note("Treat the response bytes as the artifact, hash them, compare against the digest field from the index. The artifact is the SKILL.md in file mode and the packed archive in archive mode — the verify ritual is the same either way. A mismatch indicates corruption or tampering, and per the SEP the host MUST NOT use the content.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# File mode — re-read SKILL.md, recompute sha256, compare against the index entry's digest.
WANT=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '.skills[] | select(.url=="skill://git-workflow/SKILL.md") | .digest')
GOT="sha256:$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"skill://git-workflow/SKILL.md"}}' \
  | jq -r '.result.contents[0].text' | shasum -a 256 | awk '{print $1}')"
[ "$WANT" = "$GOT" ] && echo "verified" || echo "MISMATCH"

# Archive mode — swap the URI; the body is base64-encoded under .contents[0].blob.
#   uri=skill://git-workflow.tar.gz     # or skill://git-workflow.zip
#   jq -r '.result.contents[0].blob' | base64 -d | shasum -a 256`).Default(),
			demokit.MakeVariant("go", "go", `target := uriGitWorkflow                 // file mode default
if detected.archive {
    target = "skill://git-workflow" + detected.suffix
}
result, _ := c.ReadResourceFull(target)
raw := []byte(result.Contents[0].Text)
if raw == nil {                          // archive mode returns base64 blob
    raw, _ = base64.StdEncoding.DecodeString(result.Contents[0].Blob)
}
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
			target := uriGitWorkflow
			if detected.archive {
				target = "skill://git-workflow" + detected.suffix
			}
			var want string
			for _, e := range idx.Skills {
				if e.URL == target {
					want = e.Digest
					break
				}
			}
			if want == "" {
				fmt.Printf("    %s not found in index\n", target)
				return nil
			}
			result, err := c.ReadResourceFull(target)
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
			fmt.Printf("    artifact: %s (%d bytes)\n", target, len(raw))
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
			if detected.archive {
				fmt.Printf("    Detected archive mode — per-file SKILL.md reads are unavailable; see the Archive mode section below.\n")
				return nil
			}
			body, err := c.ReadResource(uriPDFManifest)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
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
			if detected.archive {
				fmt.Printf("    Detected archive mode — supporting files surface only after unpack; see the Archive mode section below.\n")
				return nil
			}
			body, err := c.ReadResource(uriPDFRef)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
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
			if detected.archive {
				fmt.Printf("    Detected archive mode — nested-prefix skills become skill://acme/billing/refunds%s; see the Archive mode section below.\n", detected.suffix)
				return nil
			}
			body, err := c.ReadResource(uriRefundsManifest)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
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
			if detected.archive {
				fmt.Printf("    Detected archive mode — supporting files surface only after unpack; see the Archive mode section below.\n")
				return nil
			}
			body, err := c.ReadResource(uriRefundsEmail)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			previewBody(body)
			return nil
		})

	demo.Section("Cross-source reads (issues #797 + #808)",
		"`just serve` composes multiple sources into one catalog via `fsutil.NewMountFS`: bundled local skills at the FS root + an `archived/` sub-mount (a `.tar.gz` packed from one bundled skill, auto-wrapped by frontmatter name) + a `github/` sub-mount (fetched from anthropics/skills). The previous read steps exercised the local layer. These two steps probe the sub-mounts so the cross-source story is visible in the demo, not just in resource counts. Both steps gracefully skip when running against `--source=dir` (no sub-mounts present).",
	)

	demo.Step("Read a skill via the archive sub-mount (proves auto-wrap end-to-end)").
		Arrow("Host", "Server", "resources/read uri=skill://archived/git-workflow/SKILL.md").
		DashedArrow("Server", "Host", "text/markdown body (same content as skill://git-workflow/SKILL.md)").
		Note("`just serve` packs the bundled `git-workflow` skill into a tempfile tar.gz and mounts it under the `archived/` sub-mount. `OpenArchive` auto-wraps the archive's root-level SKILL.md under `git-workflow/` (matching the frontmatter name), so the served URI is `skill://archived/git-workflow/SKILL.md`. Bytes match the local copy — same skill, different transport. Recompute the digest if you want to verify.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":15,"method":"resources/read","params":{"uri":"skill://archived/git-workflow/SKILL.md"}}' \
  | jq -r '.result.contents[0].text'`).Default(),
			demokit.MakeVariant("go", "go", `body, _ := c.ReadResource("skill://archived/git-workflow/SKILL.md")
fmt.Println(body)`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			body, err := c.ReadResource("skill://archived/git-workflow/SKILL.md")
			if err != nil {
				fmt.Printf("    SKIP: %v (run with `just serve` for the multi-source demo)\n", err)
				return nil
			}
			fmt.Printf("    served via archived/ sub-mount (auto-wrapped by frontmatter name):\n")
			previewBody(body)
			return nil
		})

	demo.Step("Discover and read a skill via the github sub-mount").
		Arrow("Host", "Server", "resources/list (filter for skill://github/...)").
		Arrow("Host", "Server", "resources/read uri=<first github URI>").
		DashedArrow("Server", "Host", "content fetched from anthropics/skills at server boot").
		Note("Robust against changes in the upstream repo: instead of hardcoding a github URI, we enumerate `resources/list`, pick the first entry under the `github/` prefix, and read it. Proves the entire FetchGitHubArchive → MountFS sub-mount → resources/read chain — server reaches out to GitHub at boot, the bytes flow through the same MCP wire as everything else.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Find the first github URI in the catalog.
GH=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":16,"method":"resources/list"}' \
  | jq -r '.result.resources[].uri' | grep '^skill://github/' | head -1)
echo "$GH"
# Read it.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"resources/read\",\"params\":{\"uri\":\"$GH\"}}" \
  | jq -r '.result.contents[0].text' | head -20`).Default(),
			demokit.MakeVariant("go", "go", `defs, _ := c.ListResources(ctx)
var githubURI string
for _, d := range defs {
    if strings.HasPrefix(d.URI, "skill://github/") {
        githubURI = d.URI
        break
    }
}
body, _ := c.ReadResource(githubURI)
fmt.Println(body)`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			defs, err := c.ListResources(ctx.Ctx)
			if err != nil {
				fmt.Printf("    ERROR listing resources: %v\n", err)
				return nil
			}
			var githubURI string
			for _, d := range defs {
				if strings.HasPrefix(d.URI, "skill://github/") {
					githubURI = d.URI
					break
				}
			}
			if githubURI == "" {
				fmt.Printf("    SKIP: no skill://github/... resources in the catalog (run with `just serve` and ensure network is available)\n")
				return nil
			}
			fmt.Printf("    discovered: %s\n", githubURI)
			body, err := c.ReadResource(githubURI)
			if err != nil {
				fmt.Printf("    ERROR reading %s: %v\n", githubURI, err)
				return nil
			}
			fmt.Printf("    served via github/ sub-mount (fetched at server boot):\n")
			previewBody(body)
			return nil
		})

	demo.Section("Push-based invalidation (issue #795)",
		"`skill://index.json` carries `_meta.io.modelcontextprotocol.skills/version` — a monotonic counter the server bumps whenever skill content changes. Stateful clients also receive `notifications/resources/list_changed` when the bump happens. Stateless clients (no persistent push channel) detect the change by polling the index and observing the field. Detectors that drive the bump (fsnotify, webhook, manual sweep) are pluggable; this walkthrough uses a demo-only `_demo/refresh` tool that calls `Provider.Refresh()` directly.",
	)

	demo.Step("Read the version, refresh, observe it bump").
		Arrow("Host", "Server", "resources/read uri=skill://index.json (capture _meta version)").
		Arrow("Host", "Server", "tools/call name=_demo/refresh (server calls Provider.Refresh())").
		DashedArrow("Server", "Host", "notifications/resources/list_changed (stateful wire only)").
		Arrow("Host", "Server", "resources/read uri=skill://index.json (observe version bumped)").
		Note("The version field lives under `_meta` with the reverse-domain key `io.modelcontextprotocol.skills/version`, matching mcpkit's existing convention for extension metadata. The dual-wire story: subscribed stateful clients get the push notification; stateless clients see the same change by re-reading and comparing the version.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Read once, capture version.
V1=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":20,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '._meta["io.modelcontextprotocol.skills/version"]')

# Refresh.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"_demo/refresh","arguments":{}}}' \
  | jq -r '.result.content[0].text'

# Read again, observe bump.
V2=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":22,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '._meta["io.modelcontextprotocol.skills/version"]')

echo "before=$V1 after=$V2"`).Default(),
			demokit.MakeVariant("go", "go", `// Read once, capture version.
body, _ := c.ReadResource(skills.IndexURI)
var idx struct {
    Meta map[string]any ` + "`" + `json:"_meta"` + "`" + `
}
json.Unmarshal([]byte(body), &idx)
before, _ := idx.Meta["io.modelcontextprotocol.skills/version"].(float64)

// Trigger Refresh via the demo tool. Production deployments would call
// Provider.Refresh() directly from their own admin endpoint / webhook
// handler / fsnotify goroutine (see follow-up tickets #799 + the
// pushdown-to-templar issue).
c.CallTool("_demo/refresh", map[string]any{})

// Read again, observe bump.
body, _ = c.ReadResource(skills.IndexURI)
json.Unmarshal([]byte(body), &idx)
after, _ := idx.Meta["io.modelcontextprotocol.skills/version"].(float64)
fmt.Printf("before=%d after=%d\n", uint64(before), uint64(after))`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			before := readIndexVersion(c)
			fmt.Printf("    before refresh: version = %d\n", before)

			if _, err := c.ToolCall("_demo/refresh", map[string]any{}); err != nil {
				fmt.Printf("    ERROR calling _demo/refresh: %v\n", err)
				return nil
			}
			fmt.Printf("    called _demo/refresh — server bumped Provider.Version()\n")

			after := readIndexVersion(c)
			fmt.Printf("    after refresh:  version = %d\n", after)
			if after > before {
				fmt.Printf("    version bumped by %d — stateful subscribers also received notifications/resources/list_changed\n", after-before)
			} else {
				fmt.Printf("    WARNING: version did not advance (%d → %d)\n", before, after)
			}
			return nil
		})

	demo.Section("fsnotify-driven invalidation (issue #800)",
		"The previous step called `Provider.Refresh()` synchronously via the demo tool. Real deployments wire a Detector — fsnotify, webhook, or admin endpoint — that observes file changes and calls into the Applier on its own. `just serve` with `--watch` enables `skills.WithFSWatcher` + a 200ms coalesce window. Edit any file under `skills/` in another terminal and the server emits one `notifications/resources/list_changed` per logical change.",
	)

	demo.Step("Observe an fsnotify-driven broadcast").
		Arrow("Detector", "Server", "fsnotify Write event on skills/git-workflow/SKILL.md").
		Arrow("Server", "Server", "Provider.NotifyChangedEvents (mapped from fsnotify.Op)").
		DashedArrow("Server", "Host", "notifications/resources/list_changed (after 200ms coalesce)").
		Note("In `--non-interactive` mode this step synthesizes the edit (writes the same SKILL.md back to itself) and restores the original content; the actual broadcast still fires. In interactive mode it prompts you to edit a SKILL.md in a side terminal — the notification arrives as soon as your editor flushes the save.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("server", "bash", `# In one terminal:
just serve  # opt-in fsnotify:
            # (edit Makefile to add --watch to the serve target, or run directly:)
            # go run . --serve --watch

# In another terminal: subscribe + listen.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"watcher","version":"1.0"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -N http://localhost:8080/mcp -H "Mcp-Session-Id: $SID" -H 'Accept: text/event-stream' &

# Edit a SKILL.md and watch the SSE stream emit list_changed within 200ms.
echo "---" >> skills/git-workflow/SKILL.md`).Default(),
			demokit.MakeVariant("go", "go", `// Server-side wiring (already done in main.go when --watch is set):
provider, _ := skills.NewProvider(
    skills.WithDirectory("skills"),
    skills.WithFSWatcher(skills.WithFSWatcherErrorHandler(func(err error) {
        log.Printf("fsnotify: %v", err)
    })),
    skills.WithCoalesceWindow(200 * time.Millisecond),
)
provider.RegisterWith(srv)
defer provider.Shutdown(context.Background()) // graceful drain on signal

// The fsnotify goroutine maps Create/Write/Remove events to
// ChangeAction and calls Provider.NotifyChangedEvents internally —
// adopters don't touch the Detector directly. They opt in via the
// Provider option and receive the same notifications/resources/list_changed
// the synchronous Refresh call would produce.`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			before := readIndexVersion(c)
			fmt.Printf("    before edit: version = %d\n", before)

			target := "skills/git-workflow/SKILL.md"
			original, err := os.ReadFile(target)
			if err != nil {
				fmt.Printf("    SKIP: cannot read %s: %v (server may be in --bundle=archive mode or running from a different cwd)\n", target, err)
				return nil
			}
			if isInteractive() {
				fmt.Printf("    Edit %s in another terminal and save. Waiting up to 10s for a broadcast…\n", target)
			} else {
				fmt.Printf("    Synthesizing an edit to %s (non-interactive mode)…\n", target)
				edited := append(append([]byte{}, original...), []byte("\n<!-- demo edit -->\n")...)
				if err := os.WriteFile(target, edited, 0o644); err != nil {
					fmt.Printf("    ERROR writing %s: %v\n", target, err)
					return nil
				}
				defer func() {
					if err := os.WriteFile(target, original, 0o644); err != nil {
						fmt.Printf("    WARNING failed to restore %s: %v\n", target, err)
					}
				}()
			}

			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(300 * time.Millisecond)
				after := readIndexVersion(c)
				if after > before {
					fmt.Printf("    after edit: version = %d (bump observed via fsnotify; coalesced into one broadcast)\n", after)
					return nil
				}
			}
			fmt.Printf("    WARNING: no version bump observed within 10s — server may have been started without --watch\n")
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
			if detected.archive {
				fmt.Printf("    Detected archive mode — directoryRead lists per-file resources, which archive mode does not publish. The unpacked archive (see below) recovers the same shape.\n")
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
		Note("Activate is intra-process — no wire traffic. Run with `just serve EXPORTER=stdout` + `just demo EXPORTER=stdout` to see spans.").
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
			target := uriPDFManifest
			if detected.archive {
				target = "skill://pdf-processing" + detected.suffix
			}
			if _, err := sc.ReadAndVerify(context.Background(), target, ""); err != nil {
				fmt.Printf("    ERROR ReadAndVerify(%s): %v\n", target, err)
				return nil
			}
			fmt.Printf("    sc.ReadAndVerify(%s) emitted skills.read_and_verify span\n", target)
			ev := sc.Activate(context.Background(), target,
				skills.WithReason("walkthrough_demonstration"))
			fmt.Printf("    sc.Activate returned ActivationEvent{URI=%s, Reason=%q}\n", ev.URI, ev.Reason)
			fmt.Printf("    %d hook invocation(s)\n", activations)
			return nil
		})

	demo.Section("Archive mode — atomic delivery + in-process unpack",
		"In archive mode every skill is delivered as a single `.tar.gz` or `.zip` resource. The host hashes the archive bytes against the index digest, then unpacks in-memory to recover the post-unpack virtual namespace — same files the file-mode wire would have served piecemeal. Demonstrates pdf-processing (multi-file skill) because the unpacked listing actually shows something.",
	)

	demo.Step("Read pdf-processing archive, verify digest, unpack, list recovered files").
		Arrow("Host", "Server", "resources/read uri=skill://pdf-processing.tar.gz (or .zip)").
		DashedArrow("Server", "Host", "application/gzip OR application/zip blob").
		Note("Only meaningful in archive mode. In file mode the step prints the detected mode and exits — see the per-file read steps above for the equivalent file-mode story.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Fetch the archive blob (base64-encoded under .contents[0].blob in archive mode).
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":12,"method":"resources/read","params":{"uri":"skill://pdf-processing.tar.gz"}}' \
  | jq -r '.result.contents[0].blob' | base64 -d > /tmp/pdf.tgz

# Verify and list:
shasum -a 256 /tmp/pdf.tgz                 # compare against index entry digest
tar -tzf /tmp/pdf.tgz                      # recovered file tree`).Default(),
			demokit.MakeVariant("go", "go", `result, _ := c.ReadResourceFull("skill://pdf-processing" + detected.suffix)
raw, _ := base64.StdEncoding.DecodeString(result.Contents[0].Blob)
// hash raw, compare against index digest
files, _ := unpackArchive(detected.format, raw)
for _, f := range files {
    fmt.Printf("%s (%d bytes)\n", f.Name, len(f.Bytes))
}`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			if c == nil || len(indexBody) == 0 {
				return nil
			}
			if !detected.archive {
				fmt.Printf("    Detected file mode — archive-mode flow is gated; see the per-file read steps above for the equivalent file-mode story.\n")
				return nil
			}
			var idx skills.Index
			if err := json.Unmarshal(indexBody, &idx); err != nil {
				return nil
			}
			target := "skill://pdf-processing" + detected.suffix
			var want string
			for _, e := range idx.Skills {
				if e.URL == target {
					want = e.Digest
					break
				}
			}
			result, err := c.ReadResourceFull(target)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			raw, err := base64.StdEncoding.DecodeString(result.Contents[0].Blob)
			if err != nil {
				fmt.Printf("    ERROR: archive body did not arrive as base64 blob: %v\n", err)
				return nil
			}
			sum := sha256.Sum256(raw)
			got := "sha256:" + hex.EncodeToString(sum[:])
			fmt.Printf("    archive: %s (%d bytes)\n", target, len(raw))
			fmt.Printf("    want %s\n", want)
			fmt.Printf("    got  %s\n", got)
			if got != want {
				fmt.Printf("    DIGEST MISMATCH — content MUST NOT be used per the SEP\n")
				return nil
			}
			fmt.Printf("    digest matches — unpacking via stdlib (%s)…\n", detected.format.String())
			files, err := unpackArchive(detected.format, raw)
			if err != nil {
				fmt.Printf("    ERROR: unpack failed: %v\n", err)
				return nil
			}
			fmt.Printf("    recovered %d files:\n", len(files))
			for _, f := range files {
				fmt.Printf("      %-32s %d bytes\n", f.Name, len(f.Bytes))
			}
			for _, f := range files {
				if strings.HasSuffix(f.Name, "SKILL.md") {
					fmt.Printf("    SKILL.md preview from the unpacked archive:\n")
					previewBody(string(f.Bytes))
					break
				}
			}
			return nil
		})

	demo.Section("Wrap-up",
		"Negotiated extension, enumerated index, sniffed the distribution mode, verified one digest against the canonical artifact (SKILL.md in file mode, packed archive in archive mode), and exercised the mode-specific read flow. The same client code paths served both — only the URI shape and the post-fetch unpack step differ.",
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

// isInteractive returns false when demokit's --non-interactive flag
// is present on the command line. The fsnotify walkthrough step
// synthesizes an edit + revert in non-interactive mode so CI runs
// drive the end-to-end path without operator action; interactive
// mode prompts the operator to make a real edit so the live demo
// feels live.
func isInteractive() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--non-interactive" {
			return false
		}
	}
	return true
}

// readIndexVersion fetches skill://index.json and returns the
// _meta.io.modelcontextprotocol.skills/version counter (issue #795).
// Returns 0 when the field is absent — the field is opt-in metadata,
// not a SEP requirement, so older / non-mcpkit servers will lack it.
func readIndexVersion(c *client.Client) uint64 {
	body, err := c.ReadResource(skills.IndexURI)
	if err != nil {
		return 0
	}
	var idx struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		return 0
	}
	v, ok := idx.Meta["io.modelcontextprotocol.skills/version"]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return uint64(f)
}

// modeInfo captures the distribution shape detected from the server's
// skill://index.json. It controls which steps below take the file-mode
// path (per-file SKILL.md reads) vs the archive-mode path (one archive
// per skill, unpacked in-process to recover the same files).
type modeInfo struct {
	archive bool
	format  skills.ArchiveFormat
	suffix  string
}

// detectMode scans a parsed Index for the first archive-typed entry and
// returns the implied distribution mode. Provider.WithArchiveMode is
// per-Provider in this revision, so a mixed index is impossible today
// and the first archive sighting decides for the whole index.
func detectMode(idx skills.Index) modeInfo {
	for _, e := range idx.Skills {
		if e.Type != skills.SkillTypeArchive {
			continue
		}
		f := skills.DetectArchiveFormat(e.URL, nil)
		return modeInfo{
			archive: true,
			format:  f,
			suffix:  f.Suffix(),
		}
	}
	return modeInfo{}
}

// archiveMember is one regular file recovered from an unpacked archive.
// Name is the entry path as it appeared inside the archive (no leading
// slash; relative to the archive root). Bytes is the unpacked content.
type archiveMember struct {
	Name  string
	Bytes []byte
}

// unpackArchive decodes raw archive bytes into the list of recovered
// regular files using nothing but the stdlib. The walkthrough calls
// this after the SHA-256 digest has been verified against the index
// entry — production hosts SHOULD also enforce an unpacked-size cap
// (see skills.DefaultArchiveMaxBytes); omitted here because the demo
// fixtures are tiny and the focus is the verify-then-unpack shape.
func unpackArchive(format skills.ArchiveFormat, raw []byte) ([]archiveMember, error) {
	switch format {
	case skills.ArchiveFormatTarGz:
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		var out []archiveMember
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			out = append(out, archiveMember{Name: h.Name, Bytes: data})
		}
		return out, nil
	case skills.ArchiveFormatZip:
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, err
		}
		var out []archiveMember
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			out = append(out, archiveMember{Name: f.Name, Bytes: data})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown archive format: %v", format)
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

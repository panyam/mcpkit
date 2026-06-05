package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
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

	demo := demokit.New("MCP Skills Extension (SEP-2640) — Reference Walkthrough").
		Dir("skills").
		Description("Walks through SEP-2640, which serves Agent Skills over MCP using the existing Resources primitive. Each file in a skill directory is exposed as a `skill://` resource, the server declares the `io.modelcontextprotocol/skills` capability in `initialize`, and the well-known `skill://index.json` resource enumerates concrete skills with SHA-256 digests. mcpkit's `ext/skills.SkillProvider` walks an `io/fs.FS` once at construction and registers each file with the configured Provider.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve, file mode by default)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # skills server on :8080 in file mode",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"The default fixture under `skills/` walks three SEP-2640 examples: a single-segment skill (`git-workflow`), a single-segment skill with supporting files (`pdf-processing`), and a nested-prefix skill (`acme/billing/refunds`).",
		"",
		"Two alternative server modes flip the wire shape:",
		"",
		"```",
		"make serve-archive   # publishes each skill as one .tar.gz resource",
		"make serve-zip       # publishes each skill as one .zip resource",
		"```",
		"",
		"In archive mode `resources/list` returns one URI per skill (e.g. `skill://pdf-processing.tar.gz`) instead of N URIs per file. The post-unpack virtual namespace hosts observe is identical either way — that's the SEP's whole-skill atomic-delivery story.",
		"",
		"Optional: `make fetch-docx` clones the [`anthropics/skills`](https://github.com/anthropics/skills) repo and stages the `docx` skill into the `skills/` fixture directory before you start the server. It's a multi-file real-world skill that exercises the supporting-file paths beyond the toy fixtures.",
	)

	demo.Section("The skill:// URI scheme",
		"SEP-2640 defines `skill://<skill-path>/<file-path>` where `<skill-path>` is a `/`-separated path locating the skill directory and `<file-path>` is the file within it. The final segment of `<skill-path>` MUST equal the skill's `name` frontmatter field. Prefix segments (anything before that final segment) are an optional server-chosen organizational namespace.",
		"",
		"Examples from the SEP table that this walkthrough exercises:",
		"",
		"| Skill path             | File                  | URI                                              |",
		"| ---------------------- | --------------------- | ------------------------------------------------ |",
		"| `git-workflow`         | `SKILL.md`            | `skill://git-workflow/SKILL.md`                  |",
		"| `pdf-processing`       | `references/FORMS.md` | `skill://pdf-processing/references/FORMS.md`     |",
		"| `acme/billing/refunds` | `SKILL.md`            | `skill://acme/billing/refunds/SKILL.md`          |",
		"",
		"The `acme/billing/refunds` shape demonstrates the prefix-segment behavior: `acme/billing` is the prefix and `refunds` is the skill name. The walkthrough reads files from each shape to confirm the routing is uniform.",
	)

	demo.Section("Capability declaration",
		"Per SEP-2640's Capability Declaration section, a server advertises `io.modelcontextprotocol/skills` under `capabilities.extensions` in its `initialize` response. The value is the empty object `{}` — never an array. mcpkit's `ext/skills.SkillsExtension{}` plus `Provider.RegisterWith(srv)` handles this automatically; nothing else to wire on the server side.",
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

	demo.Section("The discovery index",
		"`skill://index.json` is the well-known enumeration resource. It's optional in the SEP (servers MAY decline to expose it) but mcpkit auto-registers it unless `WithoutIndex()` is supplied. The index has a fixed JSON shape: a `$schema` URI pinning the index version and a `skills[]` array where each entry has `type` (`skill-md` or `archive`), `description`, `url`, `name`, and `digest`.",
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

	demo.Section("The digest contract",
		"Per SEP-2640's Integrity and Verification section, each `skill-md` and `archive` entry carries a `sha256:{64-lowercase-hex}` digest computed over the artifact's raw bytes. For `skill-md` entries the artifact is the SKILL.md file itself; for `archive` entries it's the packed archive bytes. Hosts MUST verify retrieved content against this digest before using it. The walkthrough does exactly that for the `git-workflow` skill.",
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
		"Once a host has loaded a SKILL.md, the skill's body may reference supporting files via relative paths. mcpkit's `ext/skills.ResolveRelative(skillRoot, ref)` resolves these against the skill's root URI per SEP-2640's Reading section (filesystem-style resolution, escapes via `..` rejected). The walkthrough exercises both forms: the manifest, and a supporting file deep in the skill tree.",
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

	demo.Section("Wrap-up",
		"The host has now:",
		"",
		"- Negotiated the skills extension via the standard `initialize` handshake.",
		"- Enumerated every skill the server publishes by reading `skill://index.json` once.",
		"- Verified one artifact's bytes against its SHA-256 digest, satisfying the SEP MUST.",
		"- Read SKILL.md plus supporting files across single-segment and nested-prefix skill paths.",
		"",
		"To switch the same walkthrough into archive mode, restart the server with `make serve-archive`. The host code stays exactly the same; the wire-level shape switches to one `.tar.gz` resource per skill, and the digest in the index covers the packed archive bytes instead of the SKILL.md alone.",
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

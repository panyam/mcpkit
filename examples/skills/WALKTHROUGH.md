# MCP Skills Extension (SEP-2640) — Reference Walkthrough

SEP-2640 serves Agent Skills over MCP's Resources primitive: each file under a skill directory is a `skill://` URI; `skill://index.json` enumerates them with SHA-256 digests.

## What you'll learn

- **Choose the client wire mode** — Adaptive (default) probes server/discover and falls back to the initialize handshake on -32601. Stateless forces server/discover and errors if the server cannot answer. Legacy skips the probe and goes straight to initialize.
- **Connect to the skills server** — Construct the client with the chosen wire mode, then connect. After the call returns, inspect the new accessor to see which wire engaged. The curl chain below uses the legacy wire and mints a session id reused by every subsequent step; the stateless wire skips that — each call posts directly to /mcp with no Mcp-Session-Id header.
- **resources/list returns every cataloged skill URI** — In file mode the list has N entries per skill (one for SKILL.md, one per supporting file) plus the index. In archive mode it's one entry per skill plus the index.
- **Read skill://index.json** — The Indexer caches the result with a TTL and per-skill mtime invalidation. Repeated reads return the same bytes until something in a SKILL.md actually changes. The file is not on disk — mcpkit generates it from the live provider catalog on each cache miss.
- **Detect server distribution mode from the index** — In file mode every entry's type is skill-md; the host fetches SKILL.md plus any supporting files individually. In archive mode every entry's type is archive and the URL ends in .tar.gz or .zip; the host fetches one resource per skill and unpacks it in-process. The current Provider is per-mode (no mixing), so the first archive entry sighted in the index is enough to decide.
- **Verify digest by re-fetching git-workflow's canonical artifact** — Treat the response bytes as the artifact, hash them, compare against the digest field from the index. The artifact is the SKILL.md in file mode and the packed archive in archive mode — the verify ritual is the same either way. A mismatch indicates corruption or tampering, and per the SEP the host MUST NOT use the content.
- **Read the pdf-processing SKILL.md** — This skill's frontmatter carries version and tags Extra fields. mcpkit surfaces those under ResourceDef.Annotations keyed by the io.modelcontextprotocol.skills/ reverse-domain prefix.
- **Read a supporting file via skill:// (references/FORMS.md)** — Relative reference resolution: references/FORMS.md from inside pdf-processing/SKILL.md resolves to this full URI via the SDK helper that walks the skill root.
- **Read a nested-prefix skill (acme/billing/refunds)** — Demonstrates that the prefix-segment routing works end-to-end. The skill name is refunds; the acme/billing/ prefix is server-chosen and is opaque to the skill's own frontmatter.
- **Read a supporting file in the nested skill (templates/email.md)** — Same relative-reference resolution as the pdf-processing example, this time across a multi-segment prefix.
- **Read the version, refresh, observe it bump** — The version field lives under `_meta` with the reverse-domain key `io.modelcontextprotocol.skills/version`, matching mcpkit's existing convention for extension metadata. The dual-wire story: subscribed stateful clients get the push notification; stateless clients see the same change by re-reading and comparing the version.
- **Observe an fsnotify-driven broadcast** — In `--non-interactive` mode this step synthesizes the edit (writes the same SKILL.md back to itself) and restores the original content; the actual broadcast still fires. In interactive mode it prompts you to edit a SKILL.md in a side terminal — the notification arrives as soon as your editor flushes the save.
- **List a directory inside a skill and recurse into a subdirectory** — Subdirectories surface with mimeType inode/directory; the client descends by issuing a second call. The SDK wraps this into a single call; the curl below shows both round trips explicitly.
- **Wrap reads in skills.NewClient(...) and call Client.Activate** — Activate is intra-process — no wire traffic. Run with `make serve EXPORTER=stdout` + `make demo EXPORTER=stdout` to see spans.
- **Read pdf-processing archive, verify digest, unpack, list recovered files** — Only meaningful in archive mode. In file mode the step prints the detected mode and exits — see the per-file read steps above for the equivalent file-mode story.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve, file mode by default)

    Note over Host,Server: Step 1: Choose the client wire mode

    Note over Host,Server: Step 2: Connect to the skills server
    Host->>Server: POST /mcp — server/discover (stateless) OR initialize (legacy)
    Server-->>Host: serverInfo + capabilities (with extensions.skills)

    Note over Host,Server: Step 3: resources/list returns every cataloged skill URI
    Host->>Server: resources/list
    Server-->>Host: resources[] including skill://index.json + each SKILL.md

    Note over Host,Server: Step 4: Read skill://index.json
    Host->>Server: resources/read uri=skill://index.json
    Server-->>Host: { $schema, skills: [...] }

    Note over Host,Server: Step 5: Detect server distribution mode from the index

    Note over Host,Server: Step 6: Verify digest by re-fetching git-workflow's canonical artifact
    Host->>Server: resources/read uri=skill://git-workflow{/SKILL.md | .tar.gz | .zip}
    Server-->>Host: text/markdown body (file mode) OR archive bytes (archive mode)

    Note over Host,Server: Step 7: Read the pdf-processing SKILL.md
    Host->>Server: resources/read uri=skill://pdf-processing/SKILL.md
    Server-->>Host: text/markdown body

    Note over Host,Server: Step 8: Read a supporting file via skill:// (references/FORMS.md)
    Host->>Server: resources/read uri=skill://pdf-processing/references/FORMS.md
    Server-->>Host: text/markdown body

    Note over Host,Server: Step 9: Read a nested-prefix skill (acme/billing/refunds)
    Host->>Server: resources/read uri=skill://acme/billing/refunds/SKILL.md
    Server-->>Host: text/markdown body

    Note over Host,Server: Step 10: Read a supporting file in the nested skill (templates/email.md)
    Host->>Server: resources/read uri=skill://acme/billing/refunds/templates/email.md
    Server-->>Host: text/markdown body

    Note over Host,Server: Step 11: Read the version, refresh, observe it bump
    Host->>Server: resources/read uri=skill://index.json (capture _meta version)
    Host->>Server: tools/call name=_demo/refresh (server calls Provider.Refresh())
    Server-->>Host: notifications/resources/list_changed (stateful wire only)
    Host->>Server: resources/read uri=skill://index.json (observe version bumped)

    Note over Host,Server: Step 12: Observe an fsnotify-driven broadcast
    Detector->>Server: fsnotify Write event on skills/git-workflow/SKILL.md
    Server->>Server: Provider.NotifyChangedEvents (mapped from fsnotify.Op)
    Server-->>Host: notifications/resources/list_changed (after 200ms coalesce)

    Note over Host,Server: Step 13: List a directory inside a skill and recurse into a subdirectory
    Host->>Server: resources/directory/read uri=skill://acme/billing/refunds/templates
    Server-->>Host: 2 files + 1 subdirectory (`regional`, inode/directory)
    Host->>Server: resources/directory/read uri=skill://acme/billing/refunds/templates/regional
    Server-->>Host: 1 file (eu.md)

    Note over Host,Server: Step 14: Wrap reads in skills.NewClient(...) and call Client.Activate
    Host->>Server: resources/read via sc.ReadAndVerify (span: skills.read_and_verify)
    Server-->>Host: bytes + digest match

    Note over Host,Server: Step 15: Read pdf-processing archive, verify digest, unpack, list recovered files
    Host->>Server: resources/read uri=skill://pdf-processing.tar.gz (or .zip)
    Server-->>Host: application/gzip OR application/zip blob
```

## Steps

### Setup

```
Terminal 1:  make serve         # default (file mode, :8080)
             make serve-archive # one .tar.gz per skill
             make serve-zip     # one .zip per skill
Terminal 2:  make demo          # this walkthrough (--tui interactive)
```
This walkthrough auto-detects which of the three distribution modes the server is serving. The mode-aware section near the bottom shows the archive read-and-unpack flow; the file-mode read steps in the middle SKIP cleanly when archive mode is in effect (and vice versa).

### URI shape

`skill://<path>/<file>`. Final path segment = the skill's frontmatter `name`. Prefix segments (e.g. `acme/billing/`) are an optional server-chosen namespace. Walkthrough exercises `git-workflow`, `pdf-processing`, and `acme/billing/refunds`.

### Capability declaration

Server advertises `io.modelcontextprotocol/skills` under `capabilities.extensions` (always `{}`, never an array). `Provider.RegisterWith(srv)` wires it automatically.

### Wire mode (SEP-2575 dual-wire)

mcpkit's server defaults to `ModeDual` — every URL serves both the legacy `initialize` handshake and the SEP-2575 `server/discover` probe. Pick which wire the client should use; the rest of the walkthrough works identically either way.

### Step 1: Choose the client wire mode

Adaptive (default) probes server/discover and falls back to the initialize handshake on -32601. Stateless forces server/discover and errors if the server cannot answer. Legacy skips the probe and goes straight to initialize.

### Step 2: Connect to the skills server

Construct the client with the chosen wire mode, then connect. After the call returns, inspect the new accessor to see which wire engaged. The curl chain below uses the legacy wire and mints a session id reused by every subsequent step; the stateless wire skips that — each call posts directly to /mcp with no Mcp-Session-Id header.

#### Reproduce on the wire

```bash
# Legacy wire: initialize handshake mints the session id for downstream steps.
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
#     -d '{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{}}' | jq '.result'
```

### Step 3: resources/list returns every cataloged skill URI

In file mode the list has N entries per skill (one for SKILL.md, one per supporting file) plus the index. In archive mode it's one entry per skill plus the index.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"resources/list"}' | jq '.result.resources[] | "\(.uri)  [\(.mimeType)]"'
```

### Discovery index

`skill://index.json` enumerates skills with `{$schema, skills:[{type, name, description, url, digest}]}`. Optional in the SEP; mcpkit auto-registers unless `WithoutIndex()`.

### Step 4: Read skill://index.json

The Indexer caches the result with a TTL and per-skill mtime invalidation. Repeated reads return the same bytes until something in a SKILL.md actually changes. The file is not on disk — mcpkit generates it from the live provider catalog on each cache miss.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' | jq '.'
```

### Distribution mode

SEP-2640 lets a server publish each skill as either individual files (`type:skill-md`) or one packed archive per skill (`type:archive` with `.tar.gz` / `.zip` suffix). The shape is visible on the index entries — the walkthrough sniffs it once and threads the result through the rest of the steps so the file-mode and archive-mode narratives stay tidy.

### Step 5: Detect server distribution mode from the index

In file mode every entry's type is skill-md; the host fetches SKILL.md plus any supporting files individually. In archive mode every entry's type is archive and the URL ends in .tar.gz or .zip; the host fetches one resource per skill and unpacks it in-process. The current Provider is per-mode (no mixing), so the first archive entry sighted in the index is enough to decide.

#### Reproduce on the wire

```bash
# Distinct types appearing in the index — "skill-md" or "archive".
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":11,"method":"resources/read","params":{"uri":"skill://index.json"}}' \
  | jq -r '.result.contents[0].text' \
  | jq -r '[.skills[].type] | unique | join(",")'
```

### Digest contract

Each entry carries `sha256:{64hex}` over the raw artifact bytes (SKILL.md for skill-md, packed archive for archive). Hosts MUST verify before use.

### Step 6: Verify digest by re-fetching git-workflow's canonical artifact

Treat the response bytes as the artifact, hash them, compare against the digest field from the index. The artifact is the SKILL.md in file mode and the packed archive in archive mode — the verify ritual is the same either way. A mismatch indicates corruption or tampering, and per the SEP the host MUST NOT use the content.

#### Reproduce on the wire

```bash
# File mode — re-read SKILL.md, recompute sha256, compare against the index entry's digest.
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
#   jq -r '.result.contents[0].blob' | base64 -d | shasum -a 256
```

### Reading skill files

Manifest body may reference supporting files via relative paths. `skills.ResolveRelative(skillRoot, ref)` resolves them filesystem-style; `..` escapes are rejected.

### Step 7: Read the pdf-processing SKILL.md

This skill's frontmatter carries version and tags Extra fields. mcpkit surfaces those under ResourceDef.Annotations keyed by the io.modelcontextprotocol.skills/ reverse-domain prefix.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"skill://pdf-processing/SKILL.md"}}' \
  | jq -r '.result.contents[0].text'
```

### Step 8: Read a supporting file via skill:// (references/FORMS.md)

Relative reference resolution: references/FORMS.md from inside pdf-processing/SKILL.md resolves to this full URI via the SDK helper that walks the skill root.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"skill://pdf-processing/references/FORMS.md"}}' \
  | jq -r '.result.contents[0].text'
```

### Step 9: Read a nested-prefix skill (acme/billing/refunds)

Demonstrates that the prefix-segment routing works end-to-end. The skill name is refunds; the acme/billing/ prefix is server-chosen and is opaque to the skill's own frontmatter.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"skill://acme/billing/refunds/SKILL.md"}}' \
  | jq -r '.result.contents[0].text'
```

### Step 10: Read a supporting file in the nested skill (templates/email.md)

Same relative-reference resolution as the pdf-processing example, this time across a multi-segment prefix.

#### Reproduce on the wire

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":8,"method":"resources/read","params":{"uri":"skill://acme/billing/refunds/templates/email.md"}}' \
  | jq -r '.result.contents[0].text'
```

### Push-based invalidation (issue #795)

`skill://index.json` carries `_meta.io.modelcontextprotocol.skills/version` — a monotonic counter the server bumps whenever skill content changes. Stateful clients also receive `notifications/resources/list_changed` when the bump happens. Stateless clients (no persistent push channel) detect the change by polling the index and observing the field. Detectors that drive the bump (fsnotify, webhook, manual sweep) are pluggable; this walkthrough uses a demo-only `_demo/refresh` tool that calls `Provider.Refresh()` directly.

### Step 11: Read the version, refresh, observe it bump

The version field lives under `_meta` with the reverse-domain key `io.modelcontextprotocol.skills/version`, matching mcpkit's existing convention for extension metadata. The dual-wire story: subscribed stateful clients get the push notification; stateless clients see the same change by re-reading and comparing the version.

#### Reproduce on the wire

```bash
# Read once, capture version.
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

echo "before=$V1 after=$V2"
```

### fsnotify-driven invalidation (issue #800)

The previous step called `Provider.Refresh()` synchronously via the demo tool. Real deployments wire a Detector — fsnotify, webhook, or admin endpoint — that observes file changes and calls into the Applier on its own. `make serve` with `--watch` enables `skills.WithFSWatcher` + a 200ms coalesce window. Edit any file under `skills/` in another terminal and the server emits one `notifications/resources/list_changed` per logical change.

### Step 12: Observe an fsnotify-driven broadcast

In `--non-interactive` mode this step synthesizes the edit (writes the same SKILL.md back to itself) and restores the original content; the actual broadcast still fires. In interactive mode it prompts you to edit a SKILL.md in a side terminal — the notification arrives as soon as your editor flushes the save.

#### Reproduce on the wire

```bash
# In one terminal:
make serve  # opt-in fsnotify:
            # (edit Makefile to add --watch to the serve target, or run directly:)
            # go run . --serve --watch

# In another terminal: subscribe + listen.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"watcher","version":"1.0"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -N http://localhost:8080/mcp -H "Mcp-Session-Id: $SID" -H 'Accept: text/event-stream' &

# Edit a SKILL.md and watch the SSE stream emit list_changed within 200ms.
echo "---" >> skills/git-workflow/SKILL.md
```

### SEP-2640 directoryRead — scoped subtree navigation

SEP commit `2e04c48d` (2026-06-09) added `resources/directory/read` for listing a directory's direct children without enumerating the server's entire resource space. Capability-gated via `io.modelcontextprotocol/skills.directoryRead`. mcpkit's Provider auto-supports it (#781).

### Step 13: List a directory inside a skill and recurse into a subdirectory

Subdirectories surface with mimeType inode/directory; the client descends by issuing a second call. The SDK wraps this into a single call; the curl below shows both round trips explicitly.

#### Reproduce on the wire

```bash
# Level 1: list the templates/ subtree of the refunds skill.
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
  | jq '.result.resources[] | {uri, mimeType}'
```

### SEP-414 P7 — Skills observability

Fetch ≠ activation. Server `resources/read` spans now carry `mcp.skill.*` attrs (#748). Client `ext/skills.Client` emits `skills.read*` spans + `Activate(ctx, uri)` for post-cache use the wire can't see (SDK-only — no spec change).

### Step 14: Wrap reads in skills.NewClient(...) and call Client.Activate

Activate is intra-process — no wire traffic. Run with `make serve EXPORTER=stdout` + `make demo EXPORTER=stdout` to see spans.

### Archive mode — atomic delivery + in-process unpack

In archive mode every skill is delivered as a single `.tar.gz` or `.zip` resource. The host hashes the archive bytes against the index digest, then unpacks in-memory to recover the post-unpack virtual namespace — same files the file-mode wire would have served piecemeal. Demonstrates pdf-processing (multi-file skill) because the unpacked listing actually shows something.

### Step 15: Read pdf-processing archive, verify digest, unpack, list recovered files

Only meaningful in archive mode. In file mode the step prints the detected mode and exits — see the per-file read steps above for the equivalent file-mode story.

#### Reproduce on the wire

```bash
# Fetch the archive blob (base64-encoded under .contents[0].blob in archive mode).
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":12,"method":"resources/read","params":{"uri":"skill://pdf-processing.tar.gz"}}' \
  | jq -r '.result.contents[0].blob' | base64 -d > /tmp/pdf.tgz

# Verify and list:
shasum -a 256 /tmp/pdf.tgz                 # compare against index entry digest
tar -tzf /tmp/pdf.tgz                      # recovered file tree
```

### Wrap-up

Negotiated extension, enumerated index, sniffed the distribution mode, verified one digest against the canonical artifact (SKILL.md in file mode, packed archive in archive mode), and exercised the mode-specific read flow. The same client code paths served both — only the URI shape and the post-fetch unpack step differ.

## Run it

```bash
go run ./examples/skills/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/skills/ --non-interactive
```

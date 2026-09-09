# ext/skills — implementation notes

SEP-2640 skills. For host-side consumption (catalog mode, `load_skill`, origin tagging) see
`agent/NOTES.md` § Host wiring, since that is agent-layer.

---

## Data-only, and it is enforced (#839, PRs 840/842)

**A skill is data delivered over resource primitives. It is never staged to disk and never
executed.**

`TestNoCodeExecutionSurface` (`noexec_test.go`) is an AST scan that fails the build if the package
imports `os/exec` or `plugin`, or calls a filesystem-write or process-spawn function. It includes
the lower-level spawn primitives (`os.StartProcess`, `syscall.Exec` / `ForkExec` / `StartProcess`)
specifically so it cannot be bypassed by dropping below `os/exec`.

**A loader that downloads and persists remote skills to the serving folder would trip this by
design.** Such a loader belongs *outside* core `ext/skills` (opt-in, e.g. `ext/skills/loader`)
with the archive traversal / symlink / bomb guards re-applied at the write boundary and integrity
verification on fetch.

`FetchGitHubArchive` already loads from GitHub without disk staging (fetch → in-memory `SourceFS`
→ serve), so that stays the default, at least for now.

`adversarial_test.go` maps this package case-by-case onto the WG's `dangerous-skills-mcp` corpus
(the non-archive slice: digest mismatch, frontmatter, directory-read traversal) and asserts the one
known gap: **a non-archive index digest covers `SKILL.md` only, so supporting files are unpinned.**
Whole-skill-digest is still open upstream, as far as we can tell.

---

## Two examples, and neither may be renamed casually

- **`examples/skills-core`** is the minimal SEP-2640 shape: a skills file served as `skill://`
  resources plus `skill://index.json` plus tool handling, file mode only. This is the scoped-down
  core the WG blessed 2026-06-30.
- **`examples/skills`** is the full surface (archives, remote sources, fsnotify) **and** the
  `testconf-skills` conformance fixture, and `conformance/Makefile` spawns it on :18099.

**Do not rename `examples/skills`.** The conformance wiring and the module path both depend on it.

Non-UI examples must call `common.SetupRenderer(demo)` before `demo.Execute()` or `--tui` silently
falls back to the plain renderer. skills-core fixed exactly this miss in PR 842. The canonical
template is in `examples/CONVENTIONS.md`.

### The security harness (#1184, PR 1187)

`examples/skills` ships a SEP-2640 security/conformance harness (`security_demo.go`). `make
security` (`--security`) runs the host-side defenses over the fixture against an in-process
server, covering progressive disclosure, supporting-file digest (`ErrDigestMismatch` /
`ErrSupportingFileUnpinned`), resource byte budget (`ErrResourceTooLarge`), `file://` scheme
rejection (`ErrInvalidScheme`), with each step printing its SEP/threat-model anchor plus PASS/REJECT.
It doubles as `TestSecurityDemo` in CI.

**Two surfaces by design**: the `--security` harness owns its in-process server so it can stage a
real post-listing on-disk tamper. The walkthrough runs against the external `make serve`, so its
digest-mismatch step forces the mismatch with a wrong pin instead.

---

## Upstream conformance scenarios (conformance PR 330)

Three server scenarios in `src/scenarios/server/skills/`:

- `sep-2640-skills-enumeration` — `skills/list` and `skills/get`, entry schema, `resources`
  completeness, `size`, the `"dynamic"` sentinel, the 512/16 MiB limits, capability declaration.
  Renamed 2026-08-28 from `sep-2640-skills-index`, which tested the retired `skill://index.json`.
- `sep-2640-skills-manifest` — SKILL.md mimeType, metadata, final-segment-equals-name, meta prefix
- `sep-2640-skills-directory` — `resources/directory/read`, capability read from `server/discover`

`make testconf-skills` runs all three **by exact `--scenario` name**. The runner does not
prefix-match.

Cross-checked against three implementations on 2026-09-07, all green. mcpkit and the Go SDK both
score 43 at 2026-07-28 and 42 at 2025-11-25; the C# SDK scores 37 and 36 because it does not declare
`directoryRead`, so those six checks skip. Every 2025-11-25 row is exactly one lower than its
2026-07-28 counterpart because `ttlMs` and `cacheScope` are undefined below that version and the
cache-attributes check SKIPs. Per-SDK setup is in `RUNNING_SEP2640.md` on the conformance branch.

**Two scenario kinds, and the names read backwards.** In the harness's `src/types.ts`:

- `ClientScenario` means **the harness acts as a client** and the SUT is a **server**. All three
  skills scenarios are these.
- `Scenario` means **the harness stands up a server** and the SUT is a **client**. Everything under
  `src/scenarios/client/` uses this, and mcpkit already runs 41/43 against it for other SEPs.

That matters for SEP-2640's client-side MUSTs, and five are now built on PR 330 under
`src/scenarios/client/skills/`: `sep-2640-client-no-prefetch` plus `-verify-{digest,size,frontmatter,unlisted}`.
mcpkit's side is driven from `cmd/testclient` (`sep2640.go`), keyed on `MCP_CONFORMANCE_SCENARIO`
the same way the SEP-2322 and SEP-2575 drivers are. They are not unreachable "host obligations",
which is how the first extraction framed them: no-prefetch in particular reduces to whether a
request arrived.

**Rejection is detected by absence**, following `auth/resource-mismatch`. The client's contract is
"load the skill, then read its supporting file", and a verifying client aborts on the load and never
reaches the second read, so reaching it is the violation. Scenarios set `allowClientError` because a
correctly-aborting client exits non-zero. The frontmatter variant recomputes digest and size for the
tampered body, or the digest check catches it first and the scenario passes for the wrong reason.

Roughly seven more rows are reachable the same way and are not written: the directory-extends-manifest
rule, load-by-URI, the empty-listing assumption, and a two-server pair for cross-origin binding.
Tracked in #1357.

**Invocation gotcha.** Against a server that does not speak `2026-07-28`, the scenarios need
**both** flags:

```
--scenario sep-2640-skills-enumeration --spec-version 2025-11-25 --force
```

`--spec-version` selects the *wire lifecycle* (2025-x is stateful with an initialize handshake,
2026-x is stateless and asserts `MCP-Protocol-Version` with no negotiation), and `--force`
overrides the extension-applicability skip, because extension scenarios never match
`--spec-version` on their own. Without both, a 2025-x server refuses with `-32022` and the failure
reads as a missing method rather than a wire mismatch. The scenarios themselves are portable:
verified 30/30 on both wires.

**Rebuild gotcha**: after fast-forwarding `../conf-skills`, run `npm run build`. The harness runs
`node dist/index.js`, so a stale `dist/` runs the OLD scenarios and `--scenario <new-name>` silently
matches nothing.

**A passing check is not an exercised check** (2026-09-04). The directory scenario read a single
`resources/directory/read` and never followed `nextCursor`, so every child-inspection check below it
saw one page. It passed against mcpkit forever because mcpkit hardcoded its page size to zero and
never emitted a cursor. Sam Bloomberg found it by running the suite against `go-sdk#1238` with
`DirectoryOptions{PageSize: 1}`: `sep-2640-directory-read-subdir-mimetype` failed against a
*conformant* server whose subdirectory happened to land on page two. Fixed upstream with
`directoryReadAll`. The same run caught a YAML normalization bug in the go-sdk, so both directions
produced findings the same day and neither was reachable from one side.

**Settled, and it was ours to fix** (#1334, was recorded here as an open WG question). SEP-2640 and
SEP-2133 do not disagree about where `directoryRead` sits. SEP-2133 is Final and defines
`extensions` as a map of identifiers to settings objects, with no envelope and no slot for `id`,
`specVersion` or `stability`. SEP-2640's example matches. The `config` envelope was mcpkit's own
invention, so both SEPs said inline and only mcpkit said otherwise.

The cost was larger than a shape mismatch. A conformant client reads
`extensions[id].directoryRead`; under the envelope that key sat one level deeper, so the client saw
an extension declaring nothing and mcpkit's directory-read support was invisible. The conformance
suite scored that as six SKIPPED checks rather than a failure, because a server that has not
declared the flag need not serve the method. `sep-2640-skills-directory` reported 1/1 passed while
exercising none of the surface. It now runs 7/7. See `docs/SEP_2133_EXTENSIONS.md`.

## Pagination shipped unreachable until 2026-09-04 (#1356)

`skills/list` and `resources/directory/read` both hardcoded their page size to zero, meaning
"return everything, emit no cursor", with no option to change it. A server with thousands of skills
returned the whole catalog in one response and an operator had to fork to avoid it.

Not a conformance bug. The spec makes pagination optional for servers and a cursorless result is a
complete listing. The defect was that the machinery shipped unreachable: `paginateEntries` and
`paginateDirectoryRead` are both correct, the wire types carry `nextCursor`, the handlers accept a
`cursor` they could never issue, and **five green unit tests called `paginateDirectoryRead(items,
cursor, 2)` directly with a page size no caller could produce**. One wire test asserted
`NextCursor == ""`, which pinned the gap as the contract, so adding paging later would have meant
deleting a passing test.

Fixed by `WithSkillsListPageSize` and `WithDirectoryReadPageSize` (defaults still zero, so behavior
is unchanged unless a server opts in) plus `Client.ReadDirectoryAll`, since `ReadDirectory` returns
one page and leaves the cursor to the caller. `pagination_scale_test.go` is the first coverage that
pages over the wire rather than unit-testing the helper.

**The same shape is still live for the base protocol.** `server/pagination.go` sets
`defaultPageSize = 0` for `tools/list`, `resources/list`, `resources/templates/list` and
`prompts/list`. Filed as #1356 against the 1.0 freeze, because changing a server's default page size
after the freeze is a breaking wire change. The recommendation there is a non-zero default set high
(250ish) rather than the 0-or-1 framing, since the real hazard is silent truncation against clients
that ignore `nextCursor`, and a high threshold leaves every server that works today unchanged.

The lesson worth carrying past this bug: **a unit test that passes the helper a value no production
caller can produce is not coverage.** It is the same family as the thin-shadow failure class in
`conformance/NOTES.md`.

## Interop: what checks what

Two reference implementations, and each can only test the side mcpkit's own
conformance suite cannot vouch for on its own. Both were run against the
`examples/skills` fixture.

- **hf-mcp-server** (Hugging Face) is a *server*, so it exercises mcpkit's
  **client**. Run it with `HF_SKILLS_DIR` pointing at a snapshot dir holding
  `skills.json` (`{skills: [{uri, frontmatter, resources:[{uri,digest}]}]}`)
  plus the expanded skill trees; `WEB_APP_PORT` sets the port, not `PORT`.
- **fast-agent** (evalstate) is a *host*, so it exercises mcpkit's **server**.
  The sharp version of that test is not running the agent, it is feeding
  mcpkit's raw bytes to fast-agent's own Pydantic models
  (`fast_agent.mcp.skills_extension.ListSkillsResult` / `GetSkillResult`),
  since those are what a shipping host actually parses.

Both pinned SEP draft `d7490ec` as of 2026-08-31, which predates `size`,
`"dynamic"` and the per-skill limits (added 2026-08-20). So they validate the
common subset and nothing beyond it; mcpkit is first on those three and has no
external check for them.

**Two gates a third-party host hits before it ever reaches skills**, both
correct and both version-gated, so worth knowing when an interop run fails
early with something that looks unrelated:

- On protocol **2026-07-28** mcpkit enforces SEP-2243 routing headers, so every
  POST needs `Mcp-Method` mirroring the JSON-RPC method. Missing it is -32020.
- On the same version SEP-2575 requires per-request `_meta` carrying
  `io.modelcontextprotocol/protocolVersion` and friends. Missing it is -32602
  "Missing required metadata".

A host negotiating **2025-11-25** needs neither and still gets the skills
extension, which is how fast-agent reaches it today. Both gates are the spec's,
not mcpkit's.

---

**Nesting reversed** (#1336). The June text forbade a `SKILL.md` in any descendant directory; the
2026-08-21 revision permits it and defines nested semantics. Worth knowing before touching
`uri.go`: `ParseURI` was never the blocker, despite the issue saying so. It already accepted
`skill://outer/inner/SKILL.md`, because its loop only rejected `SKILL.md` at a *non-terminal*
position, which is a file-cannot-be-a-directory rule rather than a nesting rule. That loop stays,
now returning `ErrManifestNotADirectory`. The two real blockers were `SplitAt` (rejected a deeper
`SKILL.md` anywhere in the file path) and `ResolveRelative` (rejected resolving onto a nested
manifest). Both are gone.

`IsManifest` carries the SEP's "nested content is supporting content" rule for free: it is true only
when `FilePath` is exactly `["SKILL.md"]`, so an enclosing skill splitting at its own boundary sees
a nested manifest as plain markdown and will not act on its frontmatter. Addressed directly, the
same file parses as its own skill, which is the flat-publication rule.

`ErrManifestNotInRoot` survives for one caller (`provider.go`, a `SKILL.md` at a source FS root) and
was re-worded to say only that.

**The Provider rejected nesting too, separately from `ParseURI`.** `walk()` ran a sorted-prefix check
and returned `ErrNestedSkill` at construction, so the fixture never even loaded. That is gone, and
`ErrNestedSkill` with it.

The subtle part is resource ownership. A file under `outer/inner/` is claimed by both skills if each
walks its own subtree, and `registerResource` errors on a duplicate URI. The split that resolves it:
**resource registration goes to the innermost owning skill** (one file, one URI, served once), while
**the entry manifest walks the whole subtree** (`Indexer.buildResources`), so the enclosing skill
still lists the nested files. A file therefore appears in two manifests and at one URI, which is
exactly the SEP's completeness rule rather than a workaround for it.

`IsManifest` carries the "hosts MUST NOT act on a nested SKILL.md's frontmatter" rule with no
nesting-aware branch anywhere: split at the enclosing boundary it is false, addressed directly it is
true.

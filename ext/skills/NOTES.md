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

## Upstream conformance scenarios (mcpconformance PR 330)

Three server `ClientScenario`s in `src/scenarios/server/skills/`:

- `sep-2640-skills-index` — index.json shape, type enum, name, digest format, scheme
- `sep-2640-skills-manifest` — SKILL.md mimeType, metadata, final-segment-equals-name, meta prefix
- `sep-2640-skills-directory` — `resources/directory/read`; capability read from `server/discover`,
  brand-neutral dynamic discovery

`make testconf-skills` runs all three **by exact `--scenario` name**. The runner does not
prefix-match, and the old single `sep-2640-skills` name is gone.

**Rebuild gotcha**: after fast-forwarding `../conf-skills` to a new fork commit, run `npm run
build`. The harness runs `node dist/index.js`, so a stale `dist/` runs the OLD scenarios and
`--scenario <new-name>` silently "matches nothing".

Verified green against mcpkit: 6/6, 6/6, 7/7.

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

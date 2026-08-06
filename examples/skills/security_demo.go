// Security/conformance mode for the skills example: a non-interactive,
// screen-shareable harness that exercises the SEP-2640 host-side defenses over
// the bundled skills/ fixture, each step tied to a SEP / threat-model /
// WG-issue anchor. It runs against an in-process skills server (no LLM, no
// external network) so it doubles as a regression test — the integrity,
// byte-budget, and scheme-rejection steps assert the expected rejection.
//
//	go run . --security      # or: just security
//
// Steps (see README.md § Security & conformance walkthrough for the anchors):
//  1. Progressive disclosure — catalog (frontmatter-only) + on-demand body.
//  2. Supporting-file integrity — verified read passes; a post-listing tamper
//     is rejected (ErrDigestMismatch); an unlisted file is rejected
//     (ErrSupportingFileUnpinned).
//  3. Resource-fetch byte budget — an over-cap read is rejected before decode
//     (ErrResourceTooLarge).
//  4. Cross-origin scheme rejection — a file:// URI is rejected (ErrInvalidScheme).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// refundsFile is the pinned supporting file the integrity step reads, tampers,
// and re-reads. Its skill-relative path (the key the index pins under) and its
// on-disk path within the served tree.
const (
	refundsRelFile  = "templates/email.md"
	refundsDiskFile = "acme/billing/refunds/templates/email.md"
	refundsSkill    = "refunds"
	unlistedRelFile = "templates/ghost.md"
	// refundsBodyMarker is a phrase that appears in the refunds SKILL.md body
	// but NOT in its frontmatter, so the catalog (frontmatter-only) must not
	// carry it while the on-demand body must.
	refundsBodyMarker = "notification template"
)

// runSecurityDemo serves the bundled skills/ fixture from a private, mutable
// copy and runs the four security steps against it, writing a screen-shareable
// transcript to out. It returns false if any step's outcome differs from the
// SEP-mandated one, so both the binary (--security) and the test can gate on it.
func runSecurityDemo(out io.Writer) (bool, error) {
	tmp, err := os.MkdirTemp("", "skills-security-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	served := filepath.Join(tmp, "skills")
	if err := copyTree("skills", served); err != nil {
		return false, fmt.Errorf("stage fixture: %w", err)
	}

	provider, err := skills.NewProvider(skills.WithDirectory(served))
	if err != nil {
		return false, err
	}
	srv := server.NewServer(core.ServerInfo{Name: "skills-security", Version: "0.1.0"})
	provider.RegisterWith(srv)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	mcp := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "security-demo", Version: "0.1.0"})
	if err := mcp.Connect(context.Background()); err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer mcp.Close()
	sc := skills.NewClient(mcp)

	ctx := context.Background()
	r := &secReport{out: out}

	// Step 1 — progressive disclosure (experimental-ext-skills#85; mcpkit
	// catalog mode, issue 910). The catalog is frontmatter-only (one line per
	// skill); the full body is fetched on demand and digest-verified.
	r.step(1, "Progressive disclosure (frontmatter-only catalog + on-demand body)",
		"SEP-2640 progressive disclosure · experimental-ext-skills#85 · mcpkit #910")
	idx, err := sc.ListSkills(ctx)
	if err != nil {
		return false, fmt.Errorf("list skills: %w", err)
	}
	catalog := skills.CatalogBlock(idx)
	entry, ok := findSkillMD(idx, refundsSkill)
	if !ok {
		return false, fmt.Errorf("fixture missing %q skill", refundsSkill)
	}
	body, err := sc.ReadAndVerify(ctx, entry.URL, entry.Digest)
	if err != nil {
		return false, fmt.Errorf("read %q body: %w", refundsSkill, err)
	}
	bodyInCatalog := strings.Contains(catalog, refundsBodyMarker)
	bodyInBody := strings.Contains(string(body.Bytes), refundsBodyMarker)
	r.detail("catalog: %d bytes for %d skills, frontmatter only (body marker present=%v)", len(catalog), countSkillMD(idx), bodyInCatalog)
	r.detail("on demand: %q body is %d bytes, digest verified=%v", refundsSkill, len(body.Bytes), body.DigestVerified)
	r.pass(strings.Contains(catalog, refundsSkill) && !bodyInCatalog && bodyInBody && body.DigestVerified,
		"body text is absent from the catalog and arrives only via a digest-verified on-demand read")

	// Step 2 — supporting-file integrity (threat model B1). A pinned file
	// verifies; an unlisted file is refused; a post-listing tamper is rejected.
	r.step(2, "Supporting-file digest verification (verify · unpinned · tamper)",
		"threat model B1 · ErrDigestMismatch / ErrSupportingFileUnpinned · mcpkit #866")
	manifest, err := sc.ReadSkillManifest(ctx, entry.URL)
	if err != nil {
		return false, fmt.Errorf("read manifest: %w", err)
	}
	if _, err := sc.ReadSkillFileVerified(ctx, entry, manifest, refundsRelFile); err != nil {
		r.pass(false, fmt.Sprintf("verified read of pinned %q should succeed, got %v", refundsRelFile, err))
	} else {
		r.detail("verified read of pinned %q: ok", refundsRelFile)
		r.pass(true, "a pinned supporting file reads and verifies")
	}
	_, errUnpinned := sc.ReadSkillFileVerified(ctx, entry, manifest, unlistedRelFile)
	r.reject(errors.Is(errUnpinned, skills.ErrSupportingFileUnpinned), errUnpinned,
		fmt.Sprintf("unlisted %q rejected as ErrSupportingFileUnpinned", unlistedRelFile))

	// Swap the file on disk AFTER listing, WITHOUT Refresh: the index keeps the
	// original pin, the server streams the mutated bytes live, so verification
	// must now fail.
	if err := tamperFile(filepath.Join(served, refundsDiskFile)); err != nil {
		return false, fmt.Errorf("tamper: %w", err)
	}
	_, errTamper := sc.ReadSkillFileVerified(ctx, entry, manifest, refundsRelFile)
	r.reject(errors.Is(errTamper, skills.ErrDigestMismatch), errTamper,
		"post-listing tamper rejected as ErrDigestMismatch")

	// Step 3 — resource-fetch byte budget (threat model T6; the bound
	// experimental-ext-skills#831 defers — mcpkit puts it at the fetch layer).
	r.step(3, "Resource-fetch byte budget (over-cap read rejected before decode)",
		"threat model T6 · experimental-ext-skills#831 · WithMaxResourceBytes / mcpkit #867")
	capBytes := max(int64(len(body.Bytes)/2), 1)
	budgeted := skills.NewClient(mcp, skills.WithMaxResourceBytes(capBytes))
	_, errBudget := budgeted.ReadAndVerify(ctx, entry.URL, entry.Digest)
	r.detail("cap=%d bytes, %q body≈%d bytes", capBytes, refundsSkill, len(body.Bytes))
	r.reject(errors.Is(errBudget, skills.ErrResourceTooLarge), errBudget,
		"over-cap read rejected as ErrResourceTooLarge before decode")

	// Step 4 — cross-origin scheme rejection (threat model T5, adv-file-url).
	r.step(4, "Cross-origin scheme rejection (file:// URI refused)",
		"threat model T5 (adv-file-url) · ErrInvalidScheme")
	_, errScheme := skills.ParseURI("file:///etc/passwd")
	r.reject(errors.Is(errScheme, skills.ErrInvalidScheme), errScheme,
		"file:///etc/passwd rejected as ErrInvalidScheme")

	r.summary()
	return r.allOK, nil
}

// findSkillMD returns the skill-md index entry with the given frontmatter name.
func findSkillMD(idx skills.Index, name string) (skills.IndexEntry, bool) {
	for _, e := range idx.Skills {
		if e.Type == skills.SkillTypeSkillMD && e.Name == name {
			return e, true
		}
	}
	return skills.IndexEntry{}, false
}

// countSkillMD counts skill-md entries in an index (the catalog's line count).
func countSkillMD(idx skills.Index) int {
	n := 0
	for _, e := range idx.Skills {
		if e.Type == skills.SkillTypeSkillMD {
			n++
		}
	}
	return n
}

// tamperFile appends bytes to a served supporting file so its content no longer
// matches the digest pinned at index time.
func tamperFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, []byte("\n<!-- tampered -->\n")...), 0o644)
}

// copyTree recursively copies the skill fixture at src into dst with writable
// perms, so the integrity step can mutate its private copy without touching the
// committed fixture.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// secReport renders the step-by-step transcript and tracks whether every step
// produced its SEP-mandated outcome.
type secReport struct {
	out   io.Writer
	allOK bool
	init  bool
}

func (r *secReport) step(n int, title, anchor string) {
	if !r.init {
		r.allOK = true
		r.init = true
	}
	fmt.Fprintf(r.out, "\n=== Step %d — %s ===\n  anchor: %s\n", n, title, anchor)
}

func (r *secReport) detail(format string, args ...any) {
	fmt.Fprintf(r.out, "  · "+format+"\n", args...)
}

// pass records a positive-path outcome: ok true prints PASS, false marks the run failed.
func (r *secReport) pass(ok bool, msg string) {
	r.record(ok, "PASS", "FAIL", msg)
}

// reject records a defense outcome: ok true (the guard fired) prints REJECT as
// the intended result; false means the guard did NOT fire and the run failed.
func (r *secReport) reject(ok bool, got error, msg string) {
	if ok {
		fmt.Fprintf(r.out, "  ✓ REJECT — %s (%v)\n", msg, got)
		return
	}
	r.allOK = false
	fmt.Fprintf(r.out, "  ✗ FAIL — expected rejection: %s (got %v)\n", msg, got)
}

func (r *secReport) record(ok bool, okLabel, failLabel, msg string) {
	if ok {
		fmt.Fprintf(r.out, "  ✓ %s — %s\n", okLabel, msg)
		return
	}
	r.allOK = false
	fmt.Fprintf(r.out, "  ✗ %s — %s\n", failLabel, msg)
}

func (r *secReport) summary() {
	if r.allOK {
		fmt.Fprintln(r.out, "\nAll steps produced their SEP-mandated outcome.")
		return
	}
	fmt.Fprintln(r.out, "\nOne or more steps did NOT produce the expected outcome.")
}

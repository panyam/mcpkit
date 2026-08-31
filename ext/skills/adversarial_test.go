package skills_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// TestAdversarialCorpus_NonArchiveSlice validates ext/skills against the
// non-archive slice of the Skills WG's adversarial corpus,
// olaservo/dangerous-skills-mcp (forked from gricha/dangerous-skills, MIT).
// The corpus is the WG's working reference for the "pending safety guidance"
// (Ola, 2026-06-24) and feeds the threat-model deliverable the WG named at
// the 2026-06-30 meeting (discussion 2994).
//
// Each covered sub-test cites the corpus case `key` and the reviewer (Den
// Delimarsky) item, and asserts mcpkit's behavior against that case's oracle
// — the one-line statement of what a SEP-2640-conformant consumer MUST do.
// The oracles are quoted from src/adversarial/catalog.ts in the corpus repo.
//
// Scope: this exercises the ext/skills CLIENT LIBRARY over its real API
// (ListSkills / ReadAndVerify / ReadDirectory / ParseFrontmatter), the same
// paths adopters use. It is deterministic and does not hit the live Hugging
// Face endpoint (https://olaservo-dangerous-skills-mcp.hf.space/mcp), which
// is archive-heavy and requires a full host; that endpoint is for manual
// end-to-end host validation.
//
// Deliberately OUT of this slice, disclosed in the coverage sub-test so the
// mapping does not overstate what mcpkit covers:
//   - archive fixtures (traversal / symlink / hardlink / bomb / setuid /
//     non-regular / windows-paths / zip / normalization-collision) —
//     archives are DEFERRED from the scoped-down SEP (discussion 2994), and
//     mcpkit's archive guards are separately covered in archive_test.go /
//     archive_fs_test.go.
//   - host-policy fixtures (allowed-tools-grant, cross-server-read, refunds
//     name-collision, live-read-divergence, cumulative-budget) — these
//     require a full HOST (approval persistence, cross-origin trust, unpack
//     budgets); a consumer library cannot satisfy them alone.
//   - adv-supporting-file-digest-swap — COVERED: every file of a skill,
//     SKILL.md included, is pinned in the entry's resources manifest, and
//     Client.ReadFromEntry rejects a swapped file on read with
//     ErrDigestMismatch. Asserted-as-covered below (was gap G13).
func TestAdversarialCorpus_NonArchiveSlice(t *testing.T) {
	// adv-content-rotation (Den D7) + the digest MUST at SEP line 204.
	// Oracle: "A host that re-verifies MUST reject the rotated read — the
	// new bytes no longer match the index digest." mcpkit surfaces this as
	// ErrDigestMismatch from Client.ReadAndVerify.
	t.Run("adv-content-rotation/digest-mismatch", func(t *testing.T) {
		sc, _ := connectSkillsClient(t, "testdata/valid")
		entries, err := sc.ListSkillEntries(context.Background())
		if err != nil {
			t.Fatalf("ListSkillEntries: %v", err)
		}
		entry := entryFor(t, entries, "git-workflow")
		// The verified read must pass against the entry's own manifest...
		if _, err := sc.ReadFromEntry(context.Background(), entry, entry.URI); err != nil {
			t.Fatalf("ReadFromEntry on true digest: %v", err)
		}
		// ...and a read whose advertised digest no longer matches the bytes
		// (the TOCTOU/rotation shape) MUST be rejected, not used.
		_, err = sc.ReadAndVerify(context.Background(), entry.URI, "sha256:"+strings.Repeat("0", 64))
		if !errors.Is(err, skills.ErrDigestMismatch) {
			t.Errorf("rotated read: err = %v, want ErrDigestMismatch", err)
		}
	})

	// Non-archive analog of adv-archive-traversal / adv-zip-traversal (Den
	// C1). Oracle: "every entry path must resolve inside the skill dir; '..'
	// segments and absolute paths MUST be rejected." For the file-mode
	// (non-archive) delivery mcpkit ships by default, the equivalent surface
	// is resources/directory/read, which rejects '..'/'.' path segments
	// before resolving.
	t.Run("path-containment/directory-read-traversal", func(t *testing.T) {
		_, c := connectSkillsClient(t, "testdata/valid")
		assertCallError(t, callDirectoryReadErr(c, "skill://acme/billing/refunds/..", ""), "traversal segment")
		assertCallError(t, callDirectoryReadErr(c, "skill://acme/billing/refunds/../../../etc/passwd", ""), "traversal segment")
	})

	// adv-frontmatter-mismatch (Den B2). Oracle: "a host MUST re-parse the
	// SKILL.md it actually fetched ... index frontmatter is not
	// authoritative." mcpkit parses frontmatter from the fetched SKILL.md
	// bytes via ParseFrontmatter (the index is never the source of truth for
	// a skill's own frontmatter), and rejects malformed frontmatter with
	// typed sentinels — a mismatched/crafted SKILL.md cannot pass as valid.
	t.Run("adv-frontmatter-mismatch/parse-fetched-bytes", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want error
		}{
			{"missing-name", "---\ndescription: d\n---\nbody\n", skills.ErrFrontmatterMissingName},
			{"missing-description", "---\nname: n\n---\nbody\n", skills.ErrFrontmatterMissingDescription},
			{"non-mapping", "---\n- just\n- a\n- list\n---\nbody\n", skills.ErrNonMappingFrontmatter},
		}
		for _, tc := range cases {
			if _, _, err := skills.ParseFrontmatter([]byte(tc.src)); !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
			}
		}
	})

	// adv-supporting-file-digest-swap (Den B1) — COVERED. Every file of a
	// skill is pinned in the entry's resources manifest and ReadFromEntry
	// rejects a swapped file on read. This was gap G13; the assertion proves
	// coverage so a regression flags it.
	t.Run("supporting-file-digest-swap", func(t *testing.T) {
		sc, _ := connectSkillsClient(t, "testdata/valid")
		entries, err := sc.ListSkillEntries(context.Background())
		if err != nil {
			t.Fatalf("ListSkillEntries: %v", err)
		}
		entry := entryFor(t, entries, "pdf-processing")
		if len(entry.Resources.Files) < 2 {
			t.Fatal("expected supporting files in the resources manifest; gap G13 has regressed")
		}
		// Find a supporting file (not the SKILL.md itself) and point its pin
		// at a divergent digest to model a swap. The re-verifying host MUST
		// reject the read.
		var idx = -1
		for n, f := range entry.Resources.Files {
			if f.URI != entry.URI {
				idx = n
				break
			}
		}
		if idx < 0 {
			t.Fatal("no supporting file in the manifest")
		}
		tampered := entry
		tampered.Resources.Files = append([]skills.SkillResource(nil), entry.Resources.Files...)
		tampered.Resources.Files[idx].Digest = "sha256:" + strings.Repeat("0", 64)

		_, err = sc.ReadFromEntry(context.Background(), tampered, tampered.Resources.Files[idx].URI)
		if !errors.Is(err, skills.ErrDigestMismatch) {
			t.Fatalf("swapped supporting file err = %v, want ErrDigestMismatch", err)
		}
	})

	// Coverage disclosure — keeps this mapping honest for the cases mcpkit
	// intentionally does not cover in this slice.
	t.Run("coverage-disclosure", func(t *testing.T) {
		t.Log("OUT OF SLICE (archive, deferred per discussion 2994; covered in archive_test.go): " +
			"adv-archive-traversal, adv-archive-symlink-escape, adv-archive-hardlink-escape, " +
			"adv-decompression-bomb, adv-archive-setuid, adv-archive-non-regular, adv-cumulative-budget, " +
			"adv-archive-windows-paths, adv-archive-normalization-collision, adv-zip-traversal, adv-zip-symlink-escape")
		t.Log("OUT OF SLICE (host-policy, not a consumer-library concern): " +
			"adv-allowed-tools-grant, adv-cross-server-read, refunds (name-collision), adv-live-read-divergence")
	})
}

package skills_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// Issue 866: supporting files (everything under a skill directory except
// SKILL.md) must be integrity-pinned so a re-verifying host can detect a
// swapped file fetched via resources/read (WG threat model B1). The
// Indexer pins them in IndexEntry.Files; Client.ReadSkillFileVerified
// checks the served bytes against the pin on read.

const pdfManifestURI = "skill://pdf-processing/SKILL.md"

// withTamperedPin returns a copy of entry whose supporting-file pin for
// path is replaced with digest, modelling a swapped supporting file. The
// pins round-trip through FileDigests()/MetaKeyFileDigests so the copy is
// independent of the original's _meta map.
func withTamperedPin(entry skills.IndexEntry, path, digest string) skills.IndexEntry {
	files := append([]skills.FileDigest(nil), entry.FileDigests()...)
	for i := range files {
		if files[i].Path == path {
			files[i].Digest = digest
		}
	}
	tampered := entry
	tampered.Meta = map[string]any{skills.MetaKeyFileDigests: files}
	return tampered
}

func pdfEntry(t *testing.T, sc *skills.Client) skills.IndexEntry {
	t.Helper()
	idx, err := sc.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	entry, ok := idx.Lookup(pdfManifestURI)
	if !ok {
		t.Fatalf("pdf-processing not in index")
	}
	return entry
}

func TestIndexEntry_Files_Populated(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry := pdfEntry(t, sc)

	// testdata/valid/pdf-processing has two supporting files:
	// references/FORMS.md and scripts/extract.py. SKILL.md is pinned by
	// the entry Digest, not repeated in the supporting-file pins. Pins
	// live under a namespaced _meta key, read via FileDigests().
	want := map[string]bool{"references/FORMS.md": true, "scripts/extract.py": true}
	files := entry.FileDigests()
	if len(files) != len(want) {
		t.Fatalf("FileDigests count = %d, want %d: %+v", len(files), len(want), files)
	}
	for _, f := range files {
		if !want[f.Path] {
			t.Errorf("unexpected pinned path %q", f.Path)
		}
		if !strings.HasPrefix(f.Digest, "sha256:") || len(f.Digest) != len("sha256:")+64 {
			t.Errorf("path %q digest not sha256:{64-hex}: %q", f.Path, f.Digest)
		}
		if f.Path == "SKILL.md" {
			t.Errorf("SKILL.md must not appear in supporting-file pins (pinned by entry Digest)")
		}
	}
	if _, ok := entry.FileDigest("scripts/extract.py"); !ok {
		t.Errorf("FileDigest lookup miss for a pinned path")
	}
	if _, ok := entry.FileDigest("nope.txt"); ok {
		t.Errorf("FileDigest lookup hit for an unpinned path")
	}
}

func TestIndexEntry_Files_OffMode(t *testing.T) {
	// With SupportingDigestsOff the index pins SKILL.md only, so no
	// supporting-file pins are emitted and a verified read has nothing to
	// check against.
	sc, _ := connectSkillsClient(t, "testdata/valid",
		skills.WithSupportingFileDigests(skills.SupportingDigestsOff))
	entry := pdfEntry(t, sc)
	if entry.Digest == "" {
		t.Errorf("SKILL.md digest should still be pinned in off mode")
	}
	if got := entry.FileDigests(); len(got) != 0 {
		t.Errorf("off mode should emit no supporting-file pins, got %+v", got)
	}
	manifest, err := sc.ReadSkillManifest(context.Background(), pdfManifestURI)
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}
	_, err = sc.ReadSkillFileVerified(context.Background(), entry, manifest, "scripts/extract.py")
	if !errors.Is(err, skills.ErrSupportingFileUnpinned) {
		t.Fatalf("off-mode verified read err = %v, want ErrSupportingFileUnpinned", err)
	}
}

func TestClient_ReadSkillFileVerified_Match(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry := pdfEntry(t, sc)
	manifest, err := sc.ReadSkillManifest(context.Background(), pdfManifestURI)
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}

	res, err := sc.ReadSkillFileVerified(context.Background(), entry, manifest, "scripts/extract.py")
	if err != nil {
		t.Fatalf("ReadSkillFileVerified: %v", err)
	}
	if !res.DigestVerified {
		t.Errorf("DigestVerified = false, want true for a matching pin")
	}
	if len(res.Bytes) == 0 {
		t.Errorf("expected supporting-file bytes, got none")
	}
}

func TestClient_ReadSkillFileVerified_SwapRejected(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry := pdfEntry(t, sc)
	manifest, err := sc.ReadSkillManifest(context.Background(), pdfManifestURI)
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}

	// Model a swap: the pin says one thing, the served bytes are
	// different. We hold the served bytes fixed (real provider) and point
	// the pin at a divergent digest — verification compares served-hash
	// against the pin and must reject either way the divergence arises.
	tampered := withTamperedPin(entry, "scripts/extract.py", "sha256:"+strings.Repeat("0", 64))

	_, err = sc.ReadSkillFileVerified(context.Background(), tampered, manifest, "scripts/extract.py")
	if !errors.Is(err, skills.ErrDigestMismatch) {
		t.Fatalf("ReadSkillFileVerified err = %v, want ErrDigestMismatch", err)
	}
}

func TestClient_ReadSkillFileVerified_Unpinned(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry := pdfEntry(t, sc)
	manifest, err := sc.ReadSkillManifest(context.Background(), pdfManifestURI)
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}

	// A path with no pin must not silently fall back to an unverified read.
	_, err = sc.ReadSkillFileVerified(context.Background(), entry, manifest, "references/UNLISTED.md")
	if !errors.Is(err, skills.ErrSupportingFileUnpinned) {
		t.Fatalf("ReadSkillFileVerified for unpinned path err = %v, want ErrSupportingFileUnpinned", err)
	}
}

func TestClient_ReadSkillFileVerified_PathNormalization(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entry := pdfEntry(t, sc)
	manifest, err := sc.ReadSkillManifest(context.Background(), pdfManifestURI)
	if err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}

	// "./scripts/../scripts/extract.py" canonicalizes to the pinned path,
	// so the pin lookup must resolve from the canonical URI, not the raw
	// relative reference.
	res, err := sc.ReadSkillFileVerified(context.Background(), entry, manifest, "./scripts/../scripts/extract.py")
	if err != nil {
		t.Fatalf("ReadSkillFileVerified with non-canonical relPath: %v", err)
	}
	if !res.DigestVerified {
		t.Errorf("DigestVerified = false, want true after path canonicalization")
	}
}

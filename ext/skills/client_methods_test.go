package skills_test

import (
	"errors"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// entryFor returns the listed entry whose frontmatter name matches, failing
// the test when the server does not serve it.
func entryFor(t *testing.T, entries []skills.SkillEntry, name string) skills.SkillEntry {
	t.Helper()
	for _, e := range entries {
		if e.Name() == name {
			return e
		}
	}
	t.Fatalf("skill %q not in listing", name)
	return skills.SkillEntry{}
}

func TestListSkillEntries_ReturnsCompleteEntries(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries returned")
	}
	e := entryFor(t, entries, "pdf-processing")
	if e.Resources.Dynamic {
		t.Fatal("static skill reported the dynamic sentinel")
	}
	if len(e.Resources.Files) < 2 {
		t.Errorf("resources = %d entries, want the SKILL.md plus supporting files", len(e.Resources.Files))
	}
}

func TestGetSkill_ServesEntryByURI(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	want := entries[0]

	got, err := sc.GetSkill(t.Context(), want.URI)
	if err != nil {
		t.Fatalf("GetSkill(%s): %v", want.URI, err)
	}
	if got.URI != want.URI {
		t.Errorf("uri = %q, want %q", got.URI, want.URI)
	}
	if len(got.Resources.Files) != len(want.Resources.Files) {
		t.Errorf("resources = %d, want %d; skills/get and skills/list must agree",
			len(got.Resources.Files), len(want.Resources.Files))
	}
}

func TestGetSkill_UnknownURIErrors(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	if _, err := sc.GetSkill(t.Context(), "skill://not-served/SKILL.md"); err == nil {
		t.Fatal("GetSkill on an unserved URI succeeded")
	}
}

// TestReadFromEntry_VerifiesListedFile is the happy path: a URI the manifest
// lists, at the pinned length and digest, comes back verified.
func TestReadFromEntry_VerifiesListedFile(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	for _, f := range e.Resources.Files {
		res, err := sc.ReadFromEntry(t.Context(), e, f.URI)
		if err != nil {
			t.Fatalf("ReadFromEntry(%s): %v", f.URI, err)
		}
		if !res.DigestVerified {
			t.Errorf("%s: DigestVerified = false", f.URI)
		}
		if f.Size == nil {
			t.Errorf("%s: entry carries no size; mcpkit's own server must always pin one", f.URI)
		} else if int64(len(res.Bytes)) != *f.Size {
			t.Errorf("%s: read %d bytes, entry pinned %d", f.URI, len(res.Bytes), *f.Size)
		}
	}
}

// TestReadFromEntry_RejectsUnlistedURI covers the completeness rule. The
// manifest lists every file of the skill, so a URI absent from it is a file
// the skill does not contain, and reading it is a verification failure rather
// than an unpinned read.
func TestReadFromEntry_RejectsUnlistedURI(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	_, err = sc.ReadFromEntry(t.Context(), e, "skill://pdf-processing/not-listed.md")
	if !errors.Is(err, skills.ErrURINotInResources) {
		t.Errorf("err = %v, want ErrURINotInResources", err)
	}
}

// TestReadFromEntry_RejectsSizeMismatch covers the size check independently
// of the digest. The entry is doctored so the pinned length disagrees with
// the served bytes while the digest still matches, which is the case the SEP
// added size to catch: a host that skipped hashing would otherwise accept it.
func TestReadFromEntry_RejectsSizeMismatch(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	doctored := e
	doctored.Resources.Files = append([]skills.SkillResource(nil), e.Resources.Files...)
	bumped := *e.Resources.Files[0].Size + 1
	doctored.Resources.Files[0].Size = &bumped

	_, err = sc.ReadFromEntry(t.Context(), doctored, doctored.Resources.Files[0].URI)
	if !errors.Is(err, skills.ErrSizeMismatch) {
		t.Errorf("err = %v, want ErrSizeMismatch", err)
	}
}

// TestReadFromEntry_RejectsDigestMismatch keeps the digest check honest now
// that a size check runs first: a correct length must not be enough.
func TestReadFromEntry_RejectsDigestMismatch(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	doctored := e
	doctored.Resources.Files = append([]skills.SkillResource(nil), e.Resources.Files...)
	doctored.Resources.Files[0].Digest = "sha256:" + "00000000000000000000000000000000000000000000000000000000000000ff"

	_, err = sc.ReadFromEntry(t.Context(), doctored, doctored.Resources.Files[0].URI)
	if !errors.Is(err, skills.ErrDigestMismatch) {
		t.Errorf("err = %v, want ErrDigestMismatch", err)
	}
}

// TestReadFromEntry_DynamicSkipsVerification covers the sentinel. A server
// that cannot pin generated content says so explicitly, and the read
// proceeds unverified rather than being refused.
func TestReadFromEntry_DynamicSkipsVerification(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	dyn := skills.SkillEntry{URI: e.URI, Frontmatter: e.Frontmatter, Resources: skills.DynamicResources()}
	res, err := sc.ReadFromEntry(t.Context(), dyn, e.URI)
	if err != nil {
		t.Fatalf("ReadFromEntry on a dynamic entry: %v", err)
	}
	if res.DigestVerified {
		t.Error("DigestVerified = true on a dynamic entry; there was nothing to verify against")
	}
}

// TestReadFromEntry_RejectsInvalidResources covers the load rule: an entry
// carrying neither an array nor the sentinel is invalid, and a host MUST NOT
// load the skill it describes.
func TestReadFromEntry_RejectsInvalidResources(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	invalid := skills.SkillEntry{URI: "skill://x/SKILL.md", Frontmatter: map[string]any{"name": "x"}}
	_, err := sc.ReadFromEntry(t.Context(), invalid, "skill://x/SKILL.md")
	if !errors.Is(err, skills.ErrInvalidResources) {
		t.Errorf("err = %v, want ErrInvalidResources", err)
	}
}

// TestReadFromEntry_AbsentSizeSkipsCheck covers interop with servers pinned to
// a SEP revision before size existed: the length check is skipped, the digest
// is still enforced, and the read succeeds.
func TestReadFromEntry_AbsentSizeSkipsCheck(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	noSize := e
	noSize.Resources.Files = append([]skills.SkillResource(nil), e.Resources.Files...)
	noSize.Resources.Files[0].Size = nil

	res, err := sc.ReadFromEntry(t.Context(), noSize, noSize.Resources.Files[0].URI)
	if err != nil {
		t.Fatalf("ReadFromEntry with no size: %v", err)
	}
	if !res.DigestVerified {
		t.Error("DigestVerified = false; the digest must still be enforced when size is absent")
	}
}

// TestReadFromEntry_RequireSizeRejectsAbsent covers the strict opt-in.
func TestReadFromEntry_RequireSizeRejectsAbsent(t *testing.T) {
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithRequireResourceSize(true))

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	noSize := e
	noSize.Resources.Files = append([]skills.SkillResource(nil), e.Resources.Files...)
	noSize.Resources.Files[0].Size = nil

	_, err = sc.ReadFromEntry(t.Context(), noSize, noSize.Resources.Files[0].URI)
	if !errors.Is(err, skills.ErrSizeMissing) {
		t.Errorf("err = %v, want ErrSizeMissing", err)
	}
}

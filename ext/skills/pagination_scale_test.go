package skills_test

import (
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// skillDirOf returns the directory URI of the first skill in the listing,
// which is its SKILL.md URI with the final segment stripped.
func skillDirOf(t *testing.T, entries []skills.SkillEntry) string {
	t.Helper()
	for _, e := range entries {
		if i := strings.LastIndex(e.URI, "/"); i > 0 {
			return e.URI[:i]
		}
	}
	t.Fatal("no skill URI to derive a directory from")
	return ""
}

// A paginating server must still yield every entry, and the client must not
// stop at page one. Before WithSkillsListPageSize existed the page size was a
// hardcoded zero, so this path never ran on the wire.
func TestSkillsList_PaginatesAndClientFollowsCursor(t *testing.T) {
	full, _ := connectSkillsClient(t, "testdata/valid")
	want, err := full.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("baseline ListSkillEntries: %v", err)
	}
	if len(want) < 2 {
		t.Skipf("fixture has %d skills, need 2+ to page", len(want))
	}

	paged, _ := connectSkillsClient(t, "testdata/valid", skills.WithSkillsListPageSize(1))
	got, err := paged.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("paged ListSkillEntries: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("paged listing returned %d entries, want %d", len(got), len(want))
	}
}

// The mirror of the conformance-suite bug: a single ReadDirectory sees only
// page one, so ReadDirectoryAll has to follow the cursor.
func TestReadDirectoryAll_FollowsCursor(t *testing.T) {
	full, _ := connectSkillsClient(t, "testdata/valid")
	entries, err := full.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	dir := skillDirOf(t, entries)

	want, err := full.ReadDirectoryAll(t.Context(), dir)
	if err != nil {
		t.Fatalf("baseline ReadDirectoryAll: %v", err)
	}
	if len(want) < 2 {
		t.Skipf("directory %s has %d children, need 2+ to page", dir, len(want))
	}

	paged, _ := connectSkillsClient(t, "testdata/valid", skills.WithDirectoryReadPageSize(1))

	one, err := paged.ReadDirectory(t.Context(), dir)
	if err != nil {
		t.Fatalf("ReadDirectory: %v", err)
	}
	if len(one.Resources) != 1 || one.NextCursor == "" {
		t.Fatalf("page size 1 gave %d children and cursor %q; want 1 and a cursor",
			len(one.Resources), one.NextCursor)
	}

	all, err := paged.ReadDirectoryAll(t.Context(), dir)
	if err != nil {
		t.Fatalf("ReadDirectoryAll: %v", err)
	}
	if len(all) != len(want) {
		t.Errorf("paged walk returned %d children, want %d", len(all), len(want))
	}
}

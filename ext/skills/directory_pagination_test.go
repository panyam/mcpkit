package skills

import (
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestPaginateDirectoryRead_RoundTrip walks a 5-entry input through
// pageSize=2 windows and verifies (a) each page slices contiguously,
// (b) the final page returns an empty NextCursor.
func TestPaginateDirectoryRead_RoundTrip(t *testing.T) {
	items := []core.ResourceDef{
		{URI: "a"}, {URI: "b"}, {URI: "c"}, {URI: "d"}, {URI: "e"},
	}

	page1, next1 := paginateDirectoryRead(items, "", 2)
	if got := urisOf(page1); !equalSlices(got, []string{"a", "b"}) {
		t.Fatalf("page1 = %v, want [a b]", got)
	}
	if next1 == "" {
		t.Fatalf("page1 NextCursor empty, expected one")
	}

	page2, next2 := paginateDirectoryRead(items, next1, 2)
	if got := urisOf(page2); !equalSlices(got, []string{"c", "d"}) {
		t.Fatalf("page2 = %v, want [c d]", got)
	}
	if next2 == "" {
		t.Fatalf("page2 NextCursor empty, expected one")
	}

	page3, next3 := paginateDirectoryRead(items, next2, 2)
	if got := urisOf(page3); !equalSlices(got, []string{"e"}) {
		t.Fatalf("page3 = %v, want [e]", got)
	}
	if next3 != "" {
		t.Errorf("final page NextCursor = %q, want empty", next3)
	}
}

// TestPaginateDirectoryRead_EmptyInput returns an empty page with no
// cursor, even when callers pass a stale cursor from a previous run.
func TestPaginateDirectoryRead_EmptyInput(t *testing.T) {
	page, next := paginateDirectoryRead(nil, "", 100)
	if len(page) != 0 || next != "" {
		t.Errorf("page=%v next=%q, want empty+empty", page, next)
	}
	page2, next2 := paginateDirectoryRead(nil, "10", 100)
	if len(page2) != 0 || next2 != "" {
		t.Errorf("page=%v next=%q, want empty+empty", page2, next2)
	}
}

// TestPaginateDirectoryRead_MalformedCursorDefaultsToStart treats any
// non-numeric cursor as offset 0 rather than erroring — keeps the wire
// permissive on cursor decoding per the resources/list precedent.
func TestPaginateDirectoryRead_MalformedCursorDefaultsToStart(t *testing.T) {
	items := []core.ResourceDef{{URI: "a"}, {URI: "b"}}
	page, _ := paginateDirectoryRead(items, "bogus", 100)
	if len(page) != 2 {
		t.Errorf("got %d entries, want 2", len(page))
	}
}

// TestPaginateDirectoryRead_ZeroPageSizeReturnsAll pins the production
// defaultDirectoryReadPageSize = 0 contract: a non-positive pageSize
// means "return everything from the cursor offset forward in one page,
// no nextCursor." Matches server/pagination.go::paginate. Without this
// short-circuit the production default would silently truncate every
// response to an empty page.
func TestPaginateDirectoryRead_ZeroPageSizeReturnsAll(t *testing.T) {
	items := []core.ResourceDef{{URI: "a"}, {URI: "b"}, {URI: "c"}}
	page, next := paginateDirectoryRead(items, "", 0)
	if got := urisOf(page); !equalSlices(got, []string{"a", "b", "c"}) {
		t.Errorf("page = %v, want [a b c]", got)
	}
	if next != "" {
		t.Errorf("NextCursor = %q, want empty", next)
	}
}

// TestPaginateDirectoryRead_NegativePageSizeReturnsAll keeps the
// boundary tight: any non-positive value short-circuits to "all,"
// not just literal 0.
func TestPaginateDirectoryRead_NegativePageSizeReturnsAll(t *testing.T) {
	items := []core.ResourceDef{{URI: "a"}, {URI: "b"}}
	page, next := paginateDirectoryRead(items, "", -1)
	if got := urisOf(page); !equalSlices(got, []string{"a", "b"}) {
		t.Errorf("page = %v, want [a b]", got)
	}
	if next != "" {
		t.Errorf("NextCursor = %q, want empty", next)
	}
}

func urisOf(rs []core.ResourceDef) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.URI
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

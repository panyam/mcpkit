package lsp

import (
	"strings"
	"testing"
)

func entry(path string, syms ...string) *fileEntry {
	return &fileEntry{path: path, symbols: syms}
}

// TestRankPrefersWhatOthersLeanOn is the property that makes this a map rather
// than a listing. core.go is referenced by both other files and should outrank
// them; a listing ordered by name or size would not know that.
func TestRankPrefersWhatOthersLeanOn(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := writeFile(p, body); err != nil {
			t.Fatal(err)
		}
		return p
	}
	core := write("core.go", "package p\n\nfunc CentralHelper() {}\n")
	a := write("a.go", "package p\n\nfunc AlphaThing() { CentralHelper() }\n")
	b := write("b.go", "package p\n\nfunc BetaThing() { CentralHelper() }\n")

	entries := []*fileEntry{
		entry(a, "AlphaThing"),
		entry(b, "BetaThing"),
		entry(core, "CentralHelper"),
	}
	rank(entries)

	var top *fileEntry
	for _, e := range entries {
		if top == nil || e.score > top.score {
			top = e
		}
	}
	if top.path != core {
		t.Fatalf("top ranked %s, want the file the others reference", top.path)
	}
}

// TestRankIgnoresSubstringMatches pins whole-identifier matching. Without it a
// file mentioning "CentralHelperExtra" would inflate CentralHelper's rank.
func TestRankIgnoresSubstringMatches(t *testing.T) {
	if mentions("func CentralHelperExtra() {}", "CentralHelper") {
		t.Fatal("a substring must not count as a reference")
	}
	if !mentions("x := CentralHelper()", "CentralHelper") {
		t.Fatal("a whole identifier must count")
	}
	if !mentions("CentralHelper", "CentralHelper") {
		t.Fatal("a bare identifier at both edges must count")
	}
}

// TestRankDropsAmbiguousNames pins that a name declared in two files carries no
// direction and is not pointed at a guess.
func TestRankDropsAmbiguousNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := writeFile(p, body); err != nil {
			t.Fatal(err)
		}
		return p
	}
	one := write("one.go", "package p\n\nfunc Shared() {}\n")
	two := write("two.go", "package p\n\nfunc Shared() {}\n")
	user := write("user.go", "package p\n\nfunc Caller() { Shared() }\n")

	entries := []*fileEntry{entry(one, "Shared"), entry(two, "Shared"), entry(user, "Caller")}
	rank(entries)

	// With the name dropped, nothing distinguishes the two declarers.
	if entries[0].score != entries[1].score {
		t.Fatalf("an ambiguous name gave one declarer an edge: %v vs %v", entries[0].score, entries[1].score)
	}
}

func TestRenderStaysWithinBudget(t *testing.T) {
	var entries []*fileEntry
	for i := range 200 {
		entries = append(entries, entry("/repo/file"+string(rune('a'+i%26))+".go", "SymbolOne", "SymbolTwo"))
	}
	out := render(entries, 100, 0)
	if got := len(out) / 4; got > 120 {
		t.Fatalf("rendered ~%d tokens against a budget of 100", got)
	}
	if !strings.Contains(out, "more file(s) not shown") {
		t.Fatal("a truncated map must say it was truncated")
	}
}

// TestRenderReportsWhatWasNeverIndexed pins the other cap. A map that stayed
// quiet about stopping early reads as the whole repository.
func TestRenderReportsWhatWasNeverIndexed(t *testing.T) {
	out := render([]*fileEntry{entry("/repo/a.go", "Thing")}, 1000, 37)
	if !strings.Contains(out, "37 file(s) were never indexed") {
		t.Fatalf("the file cap should be reported: %s", out)
	}
}

func TestRenderEmptyWhenNothingFits(t *testing.T) {
	if got := render([]*fileEntry{entry("/repo/a.go", "Thing")}, 0, 0); got != "" {
		t.Fatalf("a budget that fits nothing should produce no section, got %q", got)
	}
}

// TestRepoMapIsEmptyUntilReady pins that an early turn is missing the map
// rather than blocked on it. The prompt builder drops empty sections.
func TestRepoMapIsEmptyUntilReady(t *testing.T) {
	m := &repoMap{}
	if got := m.get(); got != "" {
		t.Fatalf("an unbuilt map should render nothing, got %q", got)
	}
	m.set("## Repository map\n")
	if got := m.get(); got == "" {
		t.Fatal("a built map should render")
	}
}

func TestRepoMapOffByDefault(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{})
	if ext.repoMap != nil {
		t.Fatal("the map must be opt-in")
	}
	for _, sec := range ext.PromptSections() {
		if strings.Contains(sec.Section(t.Context()), "Repository map") {
			t.Fatal("no map was configured, so no map section")
		}
	}
}

// TestRankIgnoresSubstringMatchesEndToEnd covers what the unit test above
// cannot: that rank actually uses whole-identifier matching. Testing mentions
// directly passes even if the ranking calls strings.Contains instead, which a
// mutation run demonstrated.
func TestRankIgnoresSubstringMatchesEndToEnd(t *testing.T) {
	score := func(body string) float64 {
		dir := t.TempDir()
		core := dir + "/core.go"
		other := dir + "/other.go"
		if err := writeFile(core, "package p\n\nfunc CentralHelper() {}\n"); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(other, body); err != nil {
			t.Fatal(err)
		}
		entries := []*fileEntry{entry(core, "CentralHelper"), entry(other, "OtherThing")}
		rank(entries)
		return entries[0].score
	}

	real := score("package p\n\nfunc OtherThing() { CentralHelper() }\n")
	substring := score("package p\n\nfunc OtherThing() { CentralHelperExtra() }\n")

	if substring >= real {
		t.Fatalf("a substring scored %v against a real reference's %v, so ranking is not matching whole identifiers", substring, real)
	}
}

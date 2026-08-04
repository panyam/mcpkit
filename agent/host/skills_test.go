package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	skills "github.com/panyam/mcpkit/ext/skills"
)

func TestResolveSkillsMode(t *testing.T) {
	small := skills.NewIndex(skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "a"})
	var entries []skills.IndexEntry
	for i := 0; i < defaultCatalogThreshold; i++ {
		entries = append(entries, skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: fmt.Sprintf("s%d", i)})
	}
	big := skills.NewIndex(entries...)

	if resolveSkillsMode("", small) != "eager" {
		t.Fatal("auto below threshold should be eager")
	}
	if resolveSkillsMode("", big) != "catalog" {
		t.Fatal("auto at/above threshold should be catalog")
	}
	if resolveSkillsMode("catalog", small) != "catalog" {
		t.Fatal("explicit catalog should win over auto")
	}
	if resolveSkillsMode("eager", big) != "eager" {
		t.Fatal("explicit eager should win over auto")
	}
}

func TestFilterSkillsAllow(t *testing.T) {
	idx := skills.NewIndex(
		skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "alpha"},
		skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "beta"},
		skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "gamma"},
	)

	names := func(i skills.Index) []string {
		var out []string
		for _, e := range i.Skills {
			out = append(out, e.Name)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// empty allow is a passthrough (all skills, unchanged)
	if got := names(filterSkillsAllow(idx, nil)); !eq(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("nil allow should passthrough, got %v", got)
	}
	if got := names(filterSkillsAllow(idx, []string{})); !eq(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("empty allow should passthrough, got %v", got)
	}

	// subset keeps only allowed names, in original order (not allow order)
	if got := names(filterSkillsAllow(idx, []string{"gamma", "alpha"})); !eq(got, []string{"alpha", "gamma"}) {
		t.Fatalf("subset should keep allowed in index order, got %v", got)
	}

	// unknown names in allow are no-ops
	if got := names(filterSkillsAllow(idx, []string{"beta", "nope", "missing"})); !eq(got, []string{"beta"}) {
		t.Fatalf("unknown allow names should be ignored, got %v", got)
	}

	// allow that matches nothing yields an empty skill set
	if got := names(filterSkillsAllow(idx, []string{"nope"})); len(got) != 0 {
		t.Fatalf("no matches should yield empty index, got %v", got)
	}

	// the schema URI survives the rebuild
	if filterSkillsAllow(idx, []string{"alpha"}).Schema != idx.Schema {
		t.Fatal("filtered index should keep the index schema URI")
	}
}

// TestBuildInstructions covers the default system prompt: base instructions
// plus each connected server's block, in config order, skipping empties. This
// is what makes a late server's eager skills appear on the next turn.
func TestBuildInstructions(t *testing.T) {
	a := &App{
		cfg:         &Config{Instructions: "base"},
		skillBlocks: map[string]string{"s1": "block1", "s2": "block2"},
		serverOrder: []string{"s1", "s2"},
	}
	build := func() string { return a.defaultPromptBuilder().Build(context.Background()) }

	if got := build(); got != "base\n\nblock1\n\nblock2" {
		t.Fatalf("got %q", got)
	}
	// order follows serverOrder, not map iteration
	a.serverOrder = []string{"s2", "s1"}
	if got := build(); got != "base\n\nblock2\n\nblock1" {
		t.Fatalf("order must follow serverOrder: %q", got)
	}
	// a server with no block yet is skipped
	a.skillBlocks = map[string]string{"s1": "only1"}
	a.serverOrder = []string{"s1", "s2"}
	if got := build(); got != "base\n\nonly1" {
		t.Fatalf("empty block must be skipped: %q", got)
	}
}

// TestAllCatalogSkills covers the live load_skill catalog: every connected
// server's entries flattened in config order.
func TestAllCatalogSkills(t *testing.T) {
	a := &App{
		skillCatalog: map[string][]catalogSkill{
			"s1": {{serverID: "s1", entry: skills.IndexEntry{Name: "a"}}},
			"s2": {{serverID: "s2", entry: skills.IndexEntry{Name: "b"}}},
		},
		serverOrder: []string{"s1", "s2"},
	}
	got := a.allCatalogSkills()
	if len(got) != 2 || got[0].entry.Name != "a" || got[1].entry.Name != "b" {
		t.Fatalf("flatten order wrong: %+v", got)
	}
}

func TestRegisterLoadSkill(t *testing.T) {
	app := &App{
		skillBlocks:  map[string]string{},
		skillCatalog: map[string][]catalogSkill{"s": {{serverID: "s", entry: skills.IndexEntry{Name: "alpha", URL: "skill://a/SKILL.md"}}}},
		serverOrder:  []string{"s"},
	}
	multi := agent.NewMultiSource()
	if err := app.registerLoadSkill(multi); err != nil {
		t.Fatal(err)
	}
	tools, _ := multi.Tools(context.Background())
	found := false
	for _, td := range tools {
		if td.Name == "load_skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("load_skill tool not registered: %+v", tools)
	}
	// unknown name is app-state (no server call, no error)
	res, err := multi.Call(context.Background(), "load_skill", map[string]any{"name": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !strings.Contains(resultText(res), "no skill named nope") {
		t.Fatalf("unknown skill: %+v", res)
	}
}

func TestOriginHeaderAndWrap(t *testing.T) {
	h := originHeader("docs-server")
	if !strings.Contains(h, `"docs-server"`) || !strings.Contains(h, "untrusted") {
		t.Fatalf("origin header must name the server and mark it untrusted: %q", h)
	}

	// empty block stays empty (skillsSection skips it); non-empty gets the header.
	if got := withOriginHeader("s", ""); got != "" {
		t.Fatalf("empty block must stay empty: %q", got)
	}
	got := withOriginHeader("s", "## Skills\nbody")
	if !strings.HasPrefix(got, originHeader("s")) || !strings.Contains(got, "## Skills\nbody") {
		t.Fatalf("non-empty block must be header + content: %q", got)
	}

	// load_skill body carries origin + untrusted framing, body preserved verbatim.
	body := "Step 1. do the thing\nStep 2. profit"
	w := wrapSkillOrigin("evil-server", "deploy", body)
	if !strings.Contains(w, `"deploy"`) || !strings.Contains(w, `"evil-server"`) || !strings.Contains(w, "untrusted") {
		t.Fatalf("wrapped body must tag name+server+untrusted: %q", w)
	}
	if !strings.HasSuffix(w, body) {
		t.Fatalf("wrapped body must preserve the body verbatim: %q", w)
	}
	// an unnamed (URL-only) entry still tags cleanly.
	if u := wrapSkillOrigin("s", "", "x"); !strings.Contains(u, "(unnamed)") {
		t.Fatalf("empty name should render as (unnamed): %q", u)
	}
}

func TestOriginHeaderWrapsInjectedBlocks(t *testing.T) {
	idx := skills.NewIndex(skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "deploy", Description: "ship it"})
	block := withOriginHeader("prod", skills.CatalogBlock(idx))
	if !strings.HasPrefix(block, originHeader("prod")) {
		t.Fatalf("catalog block must be origin-headed: %q", block)
	}
	if !strings.Contains(block, "deploy") {
		t.Fatalf("catalog content must survive under the header: %q", block)
	}
}

func TestResolveCatalogSkill(t *testing.T) {
	cat := []catalogSkill{
		{serverID: "a", entry: skills.IndexEntry{Name: "deploy", URL: "skill://a/deploy"}},
		{serverID: "b", entry: skills.IndexEntry{Name: "deploy", URL: "skill://b/deploy"}},
		{serverID: "a", entry: skills.IndexEntry{Name: "lint", URL: "skill://a/lint"}},
	}

	// unique name → single match, no collisions
	if m, c := resolveCatalogSkill(cat, "lint", ""); m == nil || m.serverID != "a" || c != nil {
		t.Fatalf("unique name: got %+v, %v", m, c)
	}
	// cross-origin collision → no silent pick, both origins reported
	m, c := resolveCatalogSkill(cat, "deploy", "")
	if m != nil {
		t.Fatalf("collision must not silently pick: %+v", m)
	}
	if len(c) != 2 || c[0] != "a" || c[1] != "b" {
		t.Fatalf("collision must report both origins sorted: %v", c)
	}
	// server-qualified → the right origin's entry
	if m, c := resolveCatalogSkill(cat, "deploy", "b"); m == nil || m.serverID != "b" || c != nil {
		t.Fatalf("server-qualified: got %+v, %v", m, c)
	}
	// globally-unique URL resolves without ambiguity
	if m, c := resolveCatalogSkill(cat, "skill://a/deploy", ""); m == nil || m.serverID != "a" || c != nil {
		t.Fatalf("URL match: got %+v, %v", m, c)
	}
	// unknown → nothing
	if m, c := resolveCatalogSkill(cat, "nope", ""); m != nil || c != nil {
		t.Fatalf("unknown: got %+v, %v", m, c)
	}
	// same name twice from one origin → that origin reported (point model at URL)
	dup := []catalogSkill{
		{serverID: "a", entry: skills.IndexEntry{Name: "x", URL: "skill://a/x1"}},
		{serverID: "a", entry: skills.IndexEntry{Name: "x", URL: "skill://a/x2"}},
	}
	if m, c := resolveCatalogSkill(dup, "x", ""); m != nil || len(c) != 1 || c[0] != "a" {
		t.Fatalf("intra-origin dup: got %+v, %v", m, c)
	}
}

func TestLoadSkillCollisionNoSilentShadow(t *testing.T) {
	// Two servers serve "deploy"; client is nil so any attempt to read (silent
	// shadow) would panic — proving the handler stops at disambiguation.
	app := &App{
		skillBlocks: map[string]string{},
		skillCatalog: map[string][]catalogSkill{
			"alpha": {{serverID: "alpha", entry: skills.IndexEntry{Name: "deploy", URL: "skill://alpha/deploy"}}},
			"beta":  {{serverID: "beta", entry: skills.IndexEntry{Name: "deploy", URL: "skill://beta/deploy"}}},
		},
		serverOrder: []string{"alpha", "beta"},
	}
	multi := agent.NewMultiSource()
	if err := app.registerLoadSkill(multi); err != nil {
		t.Fatal(err)
	}
	res, err := multi.Call(context.Background(), "load_skill", map[string]any{"name": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "beta") {
		t.Fatalf("collision result must name both origins, no silent shadow: %q", text)
	}
}

func TestDetectSkillCollisionsLocked(t *testing.T) {
	a := &App{skillCatalog: map[string][]catalogSkill{
		"a": {{serverID: "a", entry: skills.IndexEntry{Name: "deploy"}}, {serverID: "a", entry: skills.IndexEntry{Name: "lint"}}},
		"b": {{serverID: "b", entry: skills.IndexEntry{Name: "deploy"}}},
	}}
	got := a.detectSkillCollisionsLocked("b")
	if len(got) != 1 || !strings.Contains(got[0], `"deploy"`) || !strings.Contains(got[0], "a") {
		t.Fatalf("expected a deploy collision with server a: %v", got)
	}
	// a server whose names are all unique reports nothing
	a.skillCatalog["c"] = []catalogSkill{{serverID: "c", entry: skills.IndexEntry{Name: "unique"}}}
	if got := a.detectSkillCollisionsLocked("c"); got != nil {
		t.Fatalf("no collision expected: %v", got)
	}
}

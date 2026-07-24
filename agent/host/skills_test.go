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

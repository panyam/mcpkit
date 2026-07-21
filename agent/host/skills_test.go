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

func TestRegisterLoadSkill(t *testing.T) {
	app := &App{}
	multi := agent.NewMultiSource()
	catalog := []catalogSkill{{serverID: "s", entry: skills.IndexEntry{Name: "alpha", URL: "skill://a/SKILL.md"}}}
	if err := app.registerLoadSkill(multi, catalog); err != nil {
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

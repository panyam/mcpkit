package main

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/groups"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// themes are the fixture's tool families. Each theme registers one tool per
// verb and noun pair, so 8 themes of 5 nouns and 5 verbs give 200 tools.
// Each theme is also a group, named after the theme.
var themes = []struct {
	name  string
	nouns []string
}{
	{"billing", []string{"invoice", "refund", "customer", "plan", "coupon"}},
	{"deploy", []string{"release", "environment", "pipeline", "artifact", "secret"}},
	{"search", []string{"index", "document", "query", "synonym", "ranking"}},
	{"users", []string{"account", "role", "session", "invite", "team"}},
	{"storage", []string{"bucket", "object", "snapshot", "volume", "quota"}},
	{"metrics", []string{"series", "dashboard", "panel", "recording", "label"}},
	{"alerts", []string{"alert", "rule", "silence", "route", "receiver"}},
	{"docs", []string{"page", "space", "template", "comment", "attachment"}},
}

var verbs = []struct {
	name, does string
}{
	{"list", "Lists every %s in the %s system, newest first, with paging."},
	{"get", "Fetches one %s from the %s system by its ID, with all fields."},
	{"create", "Creates a %s in the %s system and returns its new ID."},
	{"update", "Changes fields on an existing %s in the %s system."},
	{"delete", "Deletes a %s from the %s system. This cannot be undone."},
}

// toolCount is how many tools the fixture serves. Tests and the check mode
// compare against it.
var toolCount = len(themes) * 5 * len(verbs)

//go:embed skills
var skillsFS embed.FS

// fixtureTools returns the 200 tool definitions in registration order.
func fixtureTools() []core.ToolDef {
	var defs []core.ToolDef
	for _, t := range themes {
		for _, n := range t.nouns {
			for _, v := range verbs {
				defs = append(defs, core.ToolDef{
					Name:        fmt.Sprintf("%s_%s_%s", t.name, n, v.name),
					Description: fmt.Sprintf(v.does, n, t.name),
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":     map[string]any{"type": "string", "description": "The " + n + " ID. Required for get, update and delete."},
							"fields": map[string]any{"type": "object", "description": "Fields to set on the " + n + "."},
							"cursor": map[string]any{"type": "string", "description": "Paging cursor from the previous list call."},
						},
					},
				})
			}
		}
	}
	return defs
}

// billingSkill is the SKILL.md the billing group lists next to its tools.
const billingSkill = "skill://billing-refunds/SKILL.md"

// registerFixture registers the tools, the skills and the groups on srv. Every tool
// answers with one line of text, since the exercise measures what a client
// sees in the lists, not what the tools do.
func registerFixture(srv *server.Server) error {
	for _, def := range fixtureTools() {
		name := def.Name
		srv.RegisterTool(def, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult(name + ": ok"), nil
		})
	}
	sub, err := fs.Sub(skillsFS, "skills")
	if err != nil {
		return err
	}
	provider, err := skills.NewProvider(skills.WithFS(sub))
	if err != nil {
		return fmt.Errorf("skills.NewProvider: %w", err)
	}
	provider.RegisterWith(srv)
	return fixtureGroups().RegisterWith(srv)
}

// fixtureGroups puts each theme's 25 tools in a group named after the theme.
// billing also lists the billing-refunds skill. incident-triage spans three
// themes, so no group lists it, and every client sees it in
// resources/list.
func fixtureGroups() *groups.Catalog {
	byTheme := map[string][]string{}
	for _, def := range fixtureTools() {
		theme := def.Name[:strings.IndexByte(def.Name, '_')]
		byTheme[theme] = append(byTheme[theme], def.Name)
	}
	cat := groups.NewCatalog()
	for _, t := range themes {
		d := groups.Def{
			Name:        t.name,
			Description: fmt.Sprintf("Use for %s tasks, such as %s.", t.name, joinAnd(t.nouns)),
			Tools:       byTheme[t.name],
		}
		if t.name == "billing" {
			d.Instructions = "For a refund, follow the billing-refunds skill listed in this group's resources."
			d.Resources = []string{billingSkill}
		}
		if err := cat.Add(d); err != nil {
			panic(err) // the theme table is fixed, so this is a programming error
		}
	}
	return cat
}

func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
)

// defaultCatalogThreshold is the skill count at/above which auto mode ("")
// switches a server from eager full-body injection to the catalog + load_skill
// two-tier scheme, so a large skill set doesn't bloat every request.
const defaultCatalogThreshold = 10

// skillLoaderSourceID is the MultiSource id the on-demand load_skill tool
// registers under (distinct from any per-server id).
const skillLoaderSourceID = "skills-loader"

// catalogSkill pairs a catalog-mode server's skills client with one index entry,
// so the load_skill tool can ReadAndVerify that skill's body on demand.
type catalogSkill struct {
	serverID string
	client   *skills.Client
	entry    skills.IndexEntry
}

// resolveSkillsMode maps the config mode to "eager" or "catalog". An explicit
// value wins; "" auto-selects by skill-md count against defaultCatalogThreshold.
func resolveSkillsMode(mode string, idx skills.Index) string {
	switch mode {
	case "eager", "catalog":
		return mode
	default:
		n := 0
		for _, e := range idx.Skills {
			if e.Type == skills.SkillTypeSkillMD {
				n++
			}
		}
		if n >= defaultCatalogThreshold {
			return "catalog"
		}
		return "eager"
	}
}

// filterSkillsAllow narrows an index to only the entries whose Name is in the
// allow list, a hard capability boundary applied before mode resolution so both
// the injected block and the load_skill tool see only allowed skills. An empty
// (or nil) allow list is a passthrough that returns the index unchanged. The
// match is exact by Name and covers every entry type, though only skill-md
// entries reach the model downstream. Original entry order is preserved.
func filterSkillsAllow(idx skills.Index, allow []string) skills.Index {
	if len(allow) == 0 {
		return idx
	}
	want := make(map[string]struct{}, len(allow))
	for _, name := range allow {
		want[name] = struct{}{}
	}
	var kept []skills.IndexEntry
	for _, e := range idx.Skills {
		if _, ok := want[e.Name]; ok {
			kept = append(kept, e)
		}
	}
	return skills.NewIndex(kept...)
}

// loadSkillsForServer fetches a server's skill index and returns the system-
// prompt block plus, in catalog mode, the entries the load_skill tool serves.
// Servers without the skills capability return empty silently; a fetchable
// index that fails is a startup error (the server advertised skills and the
// host could not honor them). When skillsAllow is non-empty the index is
// narrowed to those skills first, so every downstream step (mode resolution,
// eager bodies, catalog, load_skill) operates on the allowed set only. Eager
// mode injects full bodies (digest-verified; per-skill failures warn and are
// excluded); catalog mode injects only name+description and defers bodies to
// load_skill.
func loadSkillsForServer(c *client.Client, serverID, mode string, skillsAllow []string, emit func(HostEvent), tp core.TracerProvider) (string, []catalogSkill, error) {
	sc := skills.NewClient(c, skills.WithTracerProvider(tp))
	if !sc.SupportsSkills() {
		return "", nil, nil
	}
	idx, err := sc.ListSkills(context.Background())
	if err != nil {
		return "", nil, fmt.Errorf("agentchat: skills index from %s: %w", serverID, err)
	}
	idx = filterSkillsAllow(idx, skillsAllow)

	if resolveSkillsMode(mode, idx) == "catalog" {
		var cat []catalogSkill
		for _, e := range idx.Skills {
			if e.Type == skills.SkillTypeSkillMD {
				cat = append(cat, catalogSkill{serverID: serverID, client: sc, entry: e})
			}
		}
		if len(cat) > 0 {
			emit(HostEvent{Kind: HostSkillsLoaded, ServerID: serverID, Loaded: len(cat)})
		}
		return skills.CatalogBlock(idx), cat, nil
	}

	loaded := sc.LoadIndex(context.Background(), idx)
	var ok int
	for _, ls := range loaded {
		if ls.Err != nil {
			emit(HostEvent{Kind: HostSkillSkipped, ServerID: serverID, URI: ls.Entry.URL, Err: ls.Err.Error()})
			continue
		}
		ok++
	}
	if ok > 0 || len(loaded) > 0 {
		emit(HostEvent{Kind: HostSkillsLoaded, ServerID: serverID, Loaded: ok, Skipped: len(loaded) - ok})
	}
	return skills.InstructionsBlock(loaded), nil, nil
}

type loadSkillArgs struct {
	// Name is the skill's name as shown in the skills catalog.
	Name string `json:"name"`
}

// registerLoadSkill adds a load_skill(name) tool over the catalog-mode skills,
// so a name+description catalog expands to full instructions only for the
// skills a conversation actually uses. The handler ReadAndVerifies the body
// (laziness never bypasses digest verification; the activation hook fires so
// hosts learn which skills earn their tokens). An unknown name is an app-state
// result, not an error, so the model can recover.
func (a *App) registerLoadSkill(multi *agent.MultiSource, catalog []catalogSkill) error {
	fs := agent.NewFuncSource()
	err := agent.AddFunc(fs, "load_skill",
		"Read the full instructions for a named skill (from the skills catalog) before using it.",
		func(ctx context.Context, in loadSkillArgs) (string, error) {
			name := strings.TrimSpace(in.Name)
			for _, cs := range catalog {
				if cs.entry.Name == name || cs.entry.URL == name {
					res, err := cs.client.ReadAndVerify(ctx, cs.entry.URL, cs.entry.Digest)
					if err != nil {
						return "", err
					}
					return string(res.Bytes), nil
				}
			}
			return "no skill named " + name + " — use a name from the skills catalog", nil
		})
	if err != nil {
		return err
	}
	return multi.Add(skillLoaderSourceID, fs)
}

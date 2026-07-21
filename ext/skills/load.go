package skills

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// LoadedSkill is one index entry's load outcome. Exactly one of Body or Err
// is meaningful: a nil Err means Body holds the verified SKILL.md bytes; a
// non-nil Err (digest mismatch, read failure, unsupported type) means the
// skill was skipped and the host should surface, not inject, it.
type LoadedSkill struct {
	Entry IndexEntry
	Body  []byte
	Err   error
}

// LoadAll fetches the discovery index and loads it via LoadIndex. The
// returned error is non-nil only when the index itself cannot be fetched;
// per-skill failures ride the results.
func (c *Client) LoadAll(ctx context.Context) ([]LoadedSkill, error) {
	idx, err := c.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	return c.LoadIndex(ctx, idx), nil
}

// LoadIndex reads every skill-md entry of idx with digest verification.
// Per-skill failures are isolated: one tampered or unreachable skill never
// poisons the batch, it just comes back with Err set so hosts can warn and
// continue. Archive entries are recorded as skipped (extraction is a host
// decision with its own security posture, not an implicit side effect of
// loading instructions).
//
// Results are ordered by entry URL so instruction assembly is deterministic
// across runs regardless of index order. Callers that fetched (or filtered)
// an index themselves use this directly; LoadAll is the fetch-then-load
// convenience.
func (c *Client) LoadIndex(ctx context.Context, idx Index) []LoadedSkill {
	out := make([]LoadedSkill, 0, len(idx.Skills))
	for _, entry := range idx.Skills {
		ls := LoadedSkill{Entry: entry}
		switch entry.Type {
		case SkillTypeSkillMD:
			res, err := c.ReadAndVerify(ctx, entry.URL, entry.Digest)
			if err != nil {
				ls.Err = err
			} else {
				ls.Body = res.Bytes
			}
		default:
			ls.Err = fmt.Errorf("skills: entry type %q is not loaded by LoadIndex; read it explicitly", entry.Type)
		}
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry.URL < out[j].Entry.URL })
	return out
}

// InstructionsBlock renders successfully loaded skills as a system-prompt
// section: a header, then each skill's name, description, and SKILL.md body.
// Failed skills are excluded (never inject unverified content); an empty or
// all-failed batch renders to the empty string so callers can append the
// result unconditionally. Ordering follows the input, which LoadAll already
// made deterministic.
// CatalogBlock renders a compact catalog of a server's skills — one line per
// skill-md entry (name + description), the two-tier alternative to
// InstructionsBlock's full-body injection (issue 910). It tells the model what
// skills exist for roughly a tenth of the tokens; the body is fetched on demand
// via a host load_skill tool. Archive entries are omitted (host-decided
// extraction, like InstructionsBlock). Returns "" when there are no skill-md
// entries.
func CatalogBlock(idx Index) string {
	var b strings.Builder
	for _, e := range idx.Skills {
		if e.Type != SkillTypeSkillMD {
			continue
		}
		name := e.Name
		if name == "" {
			name = e.URL
		}
		if e.Description != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, e.Description)
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Skills (catalog)\n\nThese skills are available. Call load_skill(name) to read a skill's full instructions before using it.\n\n" + b.String()
}

// InstructionsBlock renders the full SKILL.md body of every successfully loaded
// skill for eager injection into the system prompt.
func InstructionsBlock(loaded []LoadedSkill) string {
	var b strings.Builder
	for _, ls := range loaded {
		if ls.Err != nil || len(ls.Body) == 0 {
			continue
		}
		name := ls.Entry.Name
		if name == "" {
			name = ls.Entry.URL
		}
		fmt.Fprintf(&b, "### Skill: %s\n", name)
		if ls.Entry.Description != "" {
			fmt.Fprintf(&b, "%s\n", ls.Entry.Description)
		}
		fmt.Fprintf(&b, "\n%s\n\n", strings.TrimSpace(string(ls.Body)))
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Skills\n\nThe following skills are provided by connected servers. Follow their instructions when relevant.\n\n" + b.String()
}

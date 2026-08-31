package skills

import (
	"context"
	"sort"
	"strings"
)

// LoadedSkill is one skill's load outcome. Exactly one of Body or Err is
// meaningful: a nil Err means Body holds the verified SKILL.md bytes; a
// non-nil Err (digest mismatch, size mismatch, read failure) means the skill
// was skipped and the host should surface it rather than inject it.
type LoadedSkill struct {
	Entry SkillEntry
	Body  []byte
	Err   error
}

// LoadAll enumerates the server's skills and loads each one.
//
// The returned error is non-nil only when the enumeration itself fails;
// per-skill failures ride the results so one tampered or unreachable skill
// never poisons the batch.
//
// An empty listing is not proof the server has no skills. SEP-2640 permits a
// server to return an empty or partial listing and hosts MUST NOT read one as
// absence, so a host holding a URI from elsewhere should still reach for
// GetSkill.
func (c *Client) LoadAll(ctx context.Context) ([]LoadedSkill, error) {
	entries, err := c.ListSkillEntries(ctx)
	if err != nil {
		return nil, err
	}
	return c.LoadEntries(ctx, entries), nil
}

// LoadEntries reads each entry's SKILL.md through the full verification path:
// the URI must be listed in the entry's own resources, the served length must
// match the pinned size, and the digest must match.
//
// Per-skill failures are isolated onto each result's Err. Results are ordered
// by URI so instruction assembly is deterministic across runs regardless of
// listing order.
//
// Callers that enumerated or filtered entries themselves use this directly;
// LoadAll is the enumerate-then-load convenience.
func (c *Client) LoadEntries(ctx context.Context, entries []SkillEntry) []LoadedSkill {
	out := make([]LoadedSkill, 0, len(entries))
	for _, entry := range entries {
		ls := LoadedSkill{Entry: entry}
		res, err := c.ReadFromEntry(ctx, entry, entry.URI)
		if err != nil {
			ls.Err = err
		} else {
			ls.Body = res.Bytes
		}
		out = append(out, ls)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry.URI < out[j].Entry.URI })
	return out
}

// CatalogBlock renders a compact catalog of a server's skills, one line per
// skill (name and description).
//
// This is the two-tier alternative to InstructionsBlock's full-body injection
// (issue 910): it tells the model what exists for roughly a tenth of the
// tokens, and the body is fetched on demand via a host load_skill tool.
// Returns "" when there is nothing to list.
func CatalogBlock(entries []SkillEntry) string {
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if name == "" {
			name = e.URI
		}
		if desc := e.Description(); desc != "" {
			b.WriteString("- " + name + ": " + desc + "\n")
		} else {
			b.WriteString("- " + name + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Skills (catalog)\n\nThese skills are available. Call load_skill(name) to read a skill's full instructions before using it.\n\n" + b.String()
}

// InstructionsBlock renders the full SKILL.md body of every successfully
// loaded skill for eager injection into the system prompt.
//
// Failed skills are excluded, because injecting unverified content is the
// thing verification exists to prevent. An empty or all-failed batch renders
// to "" so callers can append the result unconditionally.
func InstructionsBlock(loaded []LoadedSkill) string {
	var b strings.Builder
	for _, ls := range loaded {
		if ls.Err != nil || len(ls.Body) == 0 {
			continue
		}
		name := ls.Entry.Name()
		if name == "" {
			name = ls.Entry.URI
		}
		b.WriteString("### Skill: " + name + "\n")
		if desc := ls.Entry.Description(); desc != "" {
			b.WriteString(desc + "\n")
		}
		b.WriteString("\n" + strings.TrimSpace(string(ls.Body)) + "\n\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Skills\n\nThe following skills are provided by connected servers. Follow their instructions when relevant.\n\n" + b.String()
}

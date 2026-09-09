package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
)

// ErrFrontmatterMismatch is returned when a fetched SKILL.md's frontmatter
// differs from the entry's. SEP-2640 weighs this the same as a digest
// mismatch: the digest proves the bytes are the ones the server pinned, not
// that the listing described them honestly.
var ErrFrontmatterMismatch = errors.New("skills: frontmatter does not match the entry — content MUST NOT be used")

// VerifyFrontmatter compares a fetched SKILL.md's frontmatter field by field
// against entry.Frontmatter. A field on one side only is a mismatch, as is a
// differing value; the error names the offending fields.
//
// Both sides are JSON-normalized first: the entry arrives as JSON (numbers are
// float64) while the YAML parser yields ints, so raw DeepEqual would report a
// spurious mismatch on any numeric field.
func VerifyFrontmatter(entry SkillEntry, skillMD []byte) error {
	fm, _, err := ParseFrontmatter(skillMD)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrFrontmatterMismatch, entry.URI, err)
	}

	got := map[string]any{"name": fm.Name, "description": fm.Description}
	maps.Copy(got, fm.Extra)

	gotNorm, err := normalizeJSON(got)
	if err != nil {
		return fmt.Errorf("%w: %s: normalizing parsed frontmatter: %w", ErrFrontmatterMismatch, entry.URI, err)
	}
	wantNorm, err := normalizeJSON(entry.Frontmatter)
	if err != nil {
		return fmt.Errorf("%w: %s: normalizing entry frontmatter: %w", ErrFrontmatterMismatch, entry.URI, err)
	}

	if diff := frontmatterDiff(wantNorm, gotNorm); len(diff) > 0 {
		return fmt.Errorf("%w: %s: %v", ErrFrontmatterMismatch, entry.URI, diff)
	}
	return nil
}

// normalizeJSON round-trips through JSON so both sides carry the same Go
// types for equivalent data.
func normalizeJSON(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// frontmatterDiff describes every field want and got disagree on, sorted.
// Empty means they match.
func frontmatterDiff(want, got map[string]any) []string {
	// Sized from want alone. The union is at most len(want)+len(got), but that
	// addition is an allocation-size computation CodeQL flags as overflowable,
	// and capacity is only a hint: the map grows to hold both either way.
	seen := make(map[string]struct{}, len(want))
	for k := range want {
		seen[k] = struct{}{}
	}
	for k := range got {
		seen[k] = struct{}{}
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var diff []string
	for _, k := range keys {
		w, inWant := want[k]
		g, inGot := got[k]
		switch {
		case inWant && !inGot:
			diff = append(diff, fmt.Sprintf("%s: entry has %v, SKILL.md omits it", k, w))
		case !inWant && inGot:
			diff = append(diff, fmt.Sprintf("%s: SKILL.md has %v, entry omits it", k, g))
		case !jsonEqual(w, g):
			diff = append(diff, fmt.Sprintf("%s: entry has %v, SKILL.md has %v", k, w, g))
		}
	}
	return diff
}

// jsonEqual compares two JSON-normalized values structurally.
func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(x) == string(y)
}

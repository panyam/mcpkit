package skills

import (
	"fmt"
	"log/slog"
)

// LimitWarning reports one skill exceeding a SEP-2640 per-skill limit.
//
// These are SHOULD NOT limits, so exceeding one is reported rather than
// refused: a server whose skill genuinely needs more is non-conformant in a
// way only its operator can weigh, and silently truncating or refusing would
// break a working deployment to satisfy advice.
type LimitWarning struct {
	// URI is the offending skill's SKILL.md URI.
	URI string

	// Limit names which ceiling was crossed, either "resources" or "size".
	Limit string

	// Got and Max are the observed and advised values, in entries for the
	// resources limit and bytes for the size limit.
	Got, Max int64
}

// Error renders the warning as a sentence suitable for a log line.
func (w LimitWarning) Error() string {
	switch w.Limit {
	case "resources":
		return fmt.Sprintf("skills: %s has %d resource entries, above the SEP-2640 advisory limit of %d", w.URI, w.Got, w.Max)
	case "size":
		return fmt.Sprintf("skills: %s totals %d bytes, above the SEP-2640 advisory limit of %d", w.URI, w.Got, w.Max)
	}
	return fmt.Sprintf("skills: %s exceeds limit %q (%d > %d)", w.URI, w.Limit, w.Got, w.Max)
}

// CheckLimits reports every entry that crosses a SEP-2640 per-skill limit:
// more than MaxResourcesPerSkill resource entries, or more than
// MaxSkillTotalBytes summed across them.
//
// Both are computed from the entry alone, so this needs no fetch. Entries with
// a "dynamic" manifest are skipped, since neither count nor total is knowable
// before a read.
//
// The SEP names "warning when a registered skill exceeds the Limits" as
// SDK-level work, which is what this is for. Callers wanting to fail closed
// can treat a non-empty result as fatal.
func CheckLimits(entries []SkillEntry) []LimitWarning {
	var out []LimitWarning
	for _, e := range entries {
		if e.Resources.Dynamic {
			continue
		}
		if n := int64(len(e.Resources.Files)); n > MaxResourcesPerSkill {
			out = append(out, LimitWarning{URI: e.URI, Limit: "resources", Got: n, Max: MaxResourcesPerSkill})
		}
		if total := e.Resources.TotalBytes(); total > MaxSkillTotalBytes {
			out = append(out, LimitWarning{URI: e.URI, Limit: "size", Got: total, Max: MaxSkillTotalBytes})
		}
	}
	return out
}

// warnOnLimits runs CheckLimits over the current catalog and logs each
// warning once, at registration.
//
// Registration is the right moment: it is the last point at which an operator
// can still change the fixture before clients see it, and checking per request
// would repeat the same warning on every skills/list.
//
// A build failure here is swallowed deliberately. This is advisory reporting,
// and the same error will surface properly on the first skills/list, where the
// caller can act on it.
func (i *Indexer) warnOnLimits() {
	entries, err := i.Entries()
	if err != nil {
		return
	}
	for _, w := range CheckLimits(entries) {
		slog.Warn(w.Error(), "uri", w.URI, "limit", w.Limit, "got", w.Got, "max", w.Max)
	}
}

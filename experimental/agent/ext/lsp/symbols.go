package lsp

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// symbolMatch is one candidate for a name the model asked about, carrying the
// qualified form so an ambiguity error can tell the candidates apart.
type symbolMatch struct {
	qualified string
	sym       documentSymbol
}

// findSymbol resolves a name to the position an editor would put a cursor at.
//
// This is the whole reason the tools take a name instead of a line and column.
// LSP addresses every request by position, and a model has no cursor: asking
// it for one means asking it to count lines and columns out of read_file
// output, in an encoding it cannot see. Resolving here moves that counting to
// the one place that can do it correctly.
//
// A name matches on its own (Get), on its qualified form (CacheWrapper.Get),
// or on whatever decorated spelling a server uses for a method, since gopls
// reports (*CacheWrapper).Get and others report it differently. Matching all
// three costs nothing and means the model's natural spelling works.
//
// Ambiguity is refused rather than resolved to the first hit. That is the
// discipline edit_file already applies to a non-unique anchor, so it is a rule
// the model has met before, and the alternative is silently navigating to the
// wrong one of two same-named methods.
func findSymbol(syms []documentSymbol, query string) (documentSymbol, error) {
	if query == "" {
		return documentSymbol{}, fmt.Errorf("symbol is required")
	}
	var matches []symbolMatch
	var all []string
	var walk func(prefix string, list []documentSymbol)
	walk = func(prefix string, list []documentSymbol) {
		for _, s := range list {
			qualified := s.Name
			if prefix != "" {
				qualified = prefix + "." + s.Name
			}
			all = append(all, qualified)
			if symbolNames(s.Name, qualified)[query] {
				matches = append(matches, symbolMatch{qualified: qualified, sym: s})
			}
			walk(qualified, s.Children)
		}
	}
	walk("", syms)

	switch len(matches) {
	case 1:
		return matches[0].sym, nil
	case 0:
		return documentSymbol{}, fmt.Errorf("no symbol named %q in this file. It declares: %s", query, joinCapped(all, 20))
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.qualified)
		}
		sort.Strings(names)
		return documentSymbol{}, fmt.Errorf("%q is ambiguous: it matches %s. Use the qualified name", query, strings.Join(names, ", "))
	}
}

// symbolNames is the set of spellings that should resolve to one symbol.
//
// The decorated form exists because servers do not agree on how to name a
// method. gopls reports (*CacheWrapper).Get, and stripping the receiver
// punctuation yields the CacheWrapper.Get a reader would write.
func symbolNames(name, qualified string) map[string]bool {
	out := map[string]bool{name: true, qualified: true}
	if undecorated := undecorate(name); undecorated != "" {
		out[undecorated] = true
		if i := strings.LastIndex(undecorated, "."); i >= 0 {
			out[undecorated[i+1:]] = true
		}
	}
	return out
}

// undecorate turns a server's receiver spelling into a plain qualified name,
// so (*CacheWrapper).Get becomes CacheWrapper.Get. Returns empty when the name
// carries no decoration.
func undecorate(name string) string {
	if !strings.ContainsAny(name, "(*)") {
		return ""
	}
	r := strings.NewReplacer("(", "", ")", "", "*", "")
	return r.Replace(name)
}

func joinCapped(names []string, limit int) string {
	sort.Strings(names)
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(names[:limit], ", "), len(names)-limit)
}

// byteColumn converts a Position.Character to a byte offset within one line.
//
// The character is counted in utf-16 code units unless the server negotiated
// otherwise at initialize, which gopls v0.23.0 declines to do: probed directly,
// it omits positionEncoding from its capabilities whether offered utf-8 alone
// or alongside utf-16, and per spec that means utf-16.
//
// So any line containing a character outside the BMP counts columns that do
// not match the bytes, and an emoji ahead of the symbol shifts everything after
// it by one. This is the only place that difference exists, because no position
// ever reaches the model.
func byteColumn(line string, character int, encoding string) int {
	if character <= 0 {
		return 0
	}
	if encoding == "utf-8" {
		if character > len(line) {
			return len(line)
		}
		return character
	}
	units := 0
	for i, r := range line {
		if units >= character {
			return i
		}
		units += len(utf16.Encode([]rune{r}))
	}
	return len(line)
}

// severityLabel names a diagnostic's level for the model. An unknown or absent
// severity is reported as an error, because a server that did not say is not a
// server saying the problem is minor.
func severityLabel(sev int) string {
	switch sev {
	case severityWarning:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "error"
	}
}

package files

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// DefaultExclude is skipped by list_files and search_files unless a Config
// overrides it.
//
// These are directories whose contents an agent almost never wants and which
// are large enough to swamp a result set. Deliberately NOT sourced from
// .gitignore: that file says what should not be committed, which is a
// different question from what is worth reading. A .env is ignored and is
// sometimes exactly what is needed; a vendored dependency is committed and is
// usually noise. Neither set contains the other, so borrowing one for the
// other quietly does the wrong thing in both directions.
var DefaultExclude = []string{
	".git", "node_modules", "vendor", "dist", "build", "target",
	".venv", "__pycache__", ".next", ".cache",
}

// Default result limits. They bound the work a call does and the size of what
// comes back; they are not a context-window mechanism. agent.OffloadingSource
// already stores oversized results out of band, and duplicating that here
// would give two truncation mechanisms disagreeing about the same output.
const (
	// DefaultListLimit caps paths returned by one list_files call.
	DefaultListLimit = 500

	// DefaultSearchLimit caps matches returned by one search_files call.
	DefaultSearchLimit = 100

	// maxSearchFileSize skips files larger than this when searching. A
	// minified bundle or a checked-in dump matches noisily and reading it is
	// most of the cost of a search.
	maxSearchFileSize = 2 << 20 // 2 MiB

	// binarySniffLen is how much of a file is examined for a NUL byte before
	// treating it as binary.
	binarySniffLen = 8192

	// maxMatchLineLen truncates a single reported line, so one minified row
	// cannot consume the whole result budget.
	maxMatchLineLen = 300
)

// walkOpts is the shared traversal configuration for both discovery tools.
type walkOpts struct {
	root    *workspaceRoot
	dir     string
	exclude []string
	limit   int
}

// walkTarget is one root and the subtree within it to traverse. A call with no
// dir produces one target per root, which is what makes a listing span the
// workspace rather than whichever root happened to be first.
type walkTarget struct {
	root *workspaceRoot
	dir  string
}

// excluded reports whether a directory name is skipped.
//
// Matching is on the base name rather than the full path, because that is what
// the default list means: `node_modules` anywhere, not only at the root. A
// caller wanting a path-anchored rule can pass a pattern and it is matched with
// path.Match against both forms.
func excluded(patterns []string, name, rel string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
		if ok, err := path.Match(p, rel); err == nil && ok {
			return true
		}
	}
	return false
}

// walkResult is what a traversal produced, including what it left out.
//
// The counts exist so the caller can say so. A truncated list that does not
// admit it reads as a complete answer, which is the same class of failure as
// an edit silently applying to a stale file.
type walkResult struct {
	paths      []string
	skippedDir []string
	truncated  bool
	total      int
}

// walk traverses one root, honouring excludes and the limit.
//
// It walks fs.WalkDir over the Root's fs.FS rather than filepath.WalkDir over
// a joined path. That is the confinement: the walk cannot leave the root, and
// a symlinked directory is reported as a plain entry rather than descended
// into, so neither an escape nor a cycle is reachable. A symlink is left in
// the listing as a name, since knowing it exists is useful; what is refused is
// reading through it.
func (s *Source) walk(o walkOpts) (walkResult, error) {
	var res walkResult
	seenDir := map[string]bool{}

	err := fs.WalkDir(o.root.h.FS(), o.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory should not abort a listing of
			// everything else. It is recorded as skipped rather than dropped.
			if d != nil && d.IsDir() {
				if !seenDir[p] {
					seenDir[p] = true
					res.skippedDir = append(res.skippedDir, p)
				}
				return fs.SkipDir
			}
			return nil
		}
		if p == o.dir {
			return nil
		}
		if d.IsDir() {
			if excluded(o.exclude, d.Name(), p) {
				if !seenDir[p] {
					seenDir[p] = true
					res.skippedDir = append(res.skippedDir, p)
				}
				return fs.SkipDir
			}
			return nil
		}
		res.total++
		if len(res.paths) >= o.limit {
			res.truncated = true
			return nil
		}
		res.paths = append(res.paths, p)
		return nil
	})
	if err != nil {
		return walkResult{}, err
	}
	return res, nil
}

// isBinary reports whether content looks binary, by the presence of a NUL byte
// in its leading bytes. It is the same heuristic grep uses, and it is a
// heuristic: a UTF-16 text file trips it, and a binary with no early NUL does
// not.
func isBinary(b []byte) bool {
	if len(b) > binarySniffLen {
		b = b[:binarySniffLen]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// compilePattern turns the query into a matcher.
//
// A pattern that does not compile is returned as an error naming it, never as
// zero matches. "No matches" and "your pattern was malformed" are answers a
// caller acts on completely differently, and a search that reports the second
// as the first sends the model looking for code that is there.
//
// Go's regexp is RE2, so there is no backtracking and a pathological pattern
// cannot hang the call.
func compilePattern(query string, literal bool) (*regexp.Regexp, error) {
	if query == "" {
		return nil, fmt.Errorf("search_files needs a non-empty query")
	}
	expr := query
	if literal {
		expr = regexp.QuoteMeta(query)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %v; pass literal:true to search for it as plain text", query, err)
	}
	return re, nil
}

// match is one hit, rendered as path:line: text.
type match struct {
	path string
	line int
	text string
}

func (m match) String() string {
	t := strings.TrimRight(m.text, "\r\n")
	t = strings.TrimLeft(t, " \t")
	if len(t) > maxMatchLineLen {
		t = t[:maxMatchLineLen] + "…"
	}
	return fmt.Sprintf("%s:%d: %s", m.path, m.line, t)
}

// targetLabel names what a listing covered, for the "no files under X" and
// error messages. Several roots have no single name, so it says so rather than
// picking one and reading as though the others were not searched.
func targetLabel(targets []walkTarget) string {
	if len(targets) == 1 {
		return targets[0].root.abs(targets[0].dir)
	}
	return fmt.Sprintf("%d workspace roots", len(targets))
}

// walkTargets resolves the optional dir argument to the subtrees to traverse.
//
// Absent means every root, because a workspace is the set of them and a
// listing that silently covered only the first would read as complete. A dir
// names one subtree of one root, resolved the same way every other path is.
func (s *Source) walkTargets(args map[string]any) ([]walkTarget, error) {
	d, _ := args["dir"].(string)
	if d == "" || d == "." || d == "/" {
		out := make([]walkTarget, 0, len(s.roots))
		for _, r := range s.roots {
			out = append(out, walkTarget{root: r, dir: "."})
		}
		return out, nil
	}
	wr, rel, err := s.resolve(d)
	if err != nil {
		return nil, err
	}
	return []walkTarget{{root: wr, dir: rel}}, nil
}

// walkAll traverses every target and renders results as absolute paths.
//
// The limit is applied to the merged list rather than per root, so raising the
// number of roots does not quietly raise the cap. Each root is walked with the
// full limit and the merge truncates, which over-collects by at most one
// limit per root and keeps the per-root walk unaware that it has siblings.
func (s *Source) walkAll(targets []walkTarget, limit int) (walkResult, error) {
	var out walkResult
	for _, t := range targets {
		res, err := s.walk(walkOpts{root: t.root, dir: t.dir, exclude: s.exclude, limit: limit})
		if err != nil {
			return out, err
		}
		for _, p := range res.paths {
			out.paths = append(out.paths, t.root.abs(p))
		}
		for _, d := range res.skippedDir {
			out.skippedDir = append(out.skippedDir, t.root.abs(d))
		}
		out.total += res.total
		out.truncated = out.truncated || res.truncated
	}
	if len(out.paths) > limit {
		out.paths = out.paths[:limit]
		out.truncated = true
	}
	return out, nil
}

// limitArg reads an optional positive limit, falling back to a default. JSON
// numbers decode as float64, so an integer argument arrives as one.
func limitArg(args map[string]any, def int) int {
	switch v := args["limit"].(type) {
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return def
}

func (s *Source) list(args map[string]any) *core.ToolResult {
	targets, err := s.walkTargets(args)
	if err != nil {
		return toolError(err.Error())
	}
	dir := targetLabel(targets)
	limit := limitArg(args, DefaultListLimit)

	res, err := s.walkAll(targets, limit)
	if err != nil {
		return toolError(escapeMessage(dir, err))
	}
	if len(res.paths) == 0 {
		return &core.ToolResult{
			Content:           []core.Content{{Type: "text", Text: fmt.Sprintf("no files under %s", dir)}},
			StructuredContent: map[string]any{"dir": dir, "paths": []string{}, "total": 0},
		}
	}

	var b strings.Builder
	b.WriteString(strings.Join(res.paths, "\n"))
	writeOmissions(&b, res, len(res.paths), res.total, "file")

	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: b.String()}},
		StructuredContent: map[string]any{
			"dir": dir, "paths": res.paths, "total": res.total, "truncated": res.truncated,
		},
	}
}

func (s *Source) search(args map[string]any) *core.ToolResult {
	query, _ := args["query"].(string)
	literal, _ := args["literal"].(bool)
	re, err := compilePattern(query, literal)
	if err != nil {
		return toolError(err.Error())
	}
	targets, err := s.walkTargets(args)
	if err != nil {
		return toolError(err.Error())
	}
	dir := targetLabel(targets)
	limit := limitArg(args, DefaultSearchLimit)

	// Every file is a candidate, so the walk is unbounded and the limit is
	// applied to matches instead.
	walked, err := s.walkAll(targets, 1<<30)
	if err != nil {
		return toolError(escapeMessage(dir, err))
	}

	var matches []match
	var binary, unreadable, oversize int
	truncated := false
	total := 0

	for _, p := range walked.paths {
		wr, rel, rerr := s.resolve(p)
		if rerr != nil {
			unreadable++
			continue
		}
		info, err := fs.Stat(wr.h.FS(), rel)
		if err != nil {
			// A symlink pointing out of the root lands here, which is the
			// point: it stays visible as a name and is never read through.
			unreadable++
			continue
		}
		if info.Size() > maxSearchFileSize {
			oversize++
			continue
		}
		content, err := fs.ReadFile(wr.h.FS(), rel)
		if err != nil {
			unreadable++
			continue
		}
		if isBinary(content) {
			binary++
			continue
		}
		hits := searchFile(re, p, content, 1<<30)
		total += len(hits)
		for _, h := range hits {
			if len(matches) >= limit {
				truncated = true
				continue
			}
			matches = append(matches, h)
		}
	}

	if len(matches) == 0 {
		msg := fmt.Sprintf("no matches for %q under %s", query, dir)
		if n := binary + unreadable + oversize; n > 0 {
			msg += fmt.Sprintf(" (%d file(s) were not searched: %d binary, %d oversize, %d unreadable)",
				n, binary, oversize, unreadable)
		}
		return &core.ToolResult{
			Content:           []core.Content{{Type: "text", Text: msg}},
			StructuredContent: map[string]any{"query": query, "dir": dir, "matches": []string{}, "total": 0},
		}
	}

	var b strings.Builder
	for i, m := range matches {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.String())
	}
	if truncated {
		fmt.Fprintf(&b, "\n\nshowing %d of %d match(es); narrow the query or raise limit", len(matches), total)
	}
	writeSkipped(&b, walked, binary, oversize, unreadable)

	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.String()
	}
	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: b.String()}},
		StructuredContent: map[string]any{
			"query": query, "dir": dir, "matches": out, "total": total, "truncated": truncated,
		},
	}
}

// writeOmissions appends what a listing left out.
//
// Silence here would be the bug: a capped list that does not say so reads as
// the complete contents of the directory, and the caller concludes a file it
// was looking for does not exist.
func writeOmissions(b *strings.Builder, res walkResult, shown, total int, noun string) {
	if res.truncated {
		fmt.Fprintf(b, "\n\nshowing %d of %d %s(s); pass a narrower dir or raise limit", shown, total, noun)
	}
	if len(res.skippedDir) > 0 {
		fmt.Fprintf(b, "\n%d director%s skipped: %s", len(res.skippedDir),
			plural(len(res.skippedDir)), strings.Join(res.skippedDir, ", "))
	}
}

// writeSkipped appends what a search declined to read.
func writeSkipped(b *strings.Builder, walked walkResult, binary, oversize, unreadable int) {
	if n := binary + oversize + unreadable; n > 0 {
		fmt.Fprintf(b, "\n\n%d file(s) not searched: %d binary, %d over %d MiB, %d unreadable",
			n, binary, oversize, maxSearchFileSize>>20, unreadable)
	}
	if len(walked.skippedDir) > 0 {
		fmt.Fprintf(b, "\n%d director%s skipped: %s", len(walked.skippedDir),
			plural(len(walked.skippedDir)), strings.Join(walked.skippedDir, ", "))
	}
}

func plural(n int) string {
	if n == 1 {
		return "y was"
	}
	return "ies were"
}

// searchFile scans one file's content for the pattern, appending up to the
// remaining budget.
func searchFile(re *regexp.Regexp, p string, content []byte, budget int) []match {
	var out []match
	for i, line := range strings.Split(string(content), "\n") {
		if len(out) >= budget {
			break
		}
		if re.MatchString(line) {
			out = append(out, match{path: p, line: i + 1, text: line})
		}
	}
	return out
}

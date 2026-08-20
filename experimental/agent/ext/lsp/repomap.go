package lsp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// RepoMapConfig turns on the repository map. A nil pointer on Config leaves it
// off, and off contributes nothing.
type RepoMapConfig struct {
	// TokenBudget caps the rendered map. Zero means DefaultRepoMapBudget.
	//
	// Counted as bytes/4, which is a rough proxy and deliberately not a
	// tokenizer: the map is prose for orientation, and being 20% out costs a
	// little context rather than correctness.
	TokenBudget int

	// MaxFiles caps how many files are asked about. Zero means
	// DefaultRepoMapMaxFiles.
	//
	// Every file costs a didOpen and a documentSymbol round trip, so this is
	// the difference between indexing a repository and hanging on one. What
	// the cap drops is reported in the map.
	MaxFiles int

	// Exclude names directories to skip, by base name. Nil means
	// DefaultRepoMapExclude.
	Exclude []string
}

// Defaults for the repository map.
const (
	DefaultRepoMapBudget   = 2000
	DefaultRepoMapMaxFiles = 400
)

// DefaultRepoMapExclude is the usual noise: dependency trees, build output, and
// version control. Not read from .gitignore, which answers a different question
// (see files.DefaultExclude for the same reasoning).
var DefaultRepoMapExclude = []string{
	".git", "node_modules", "vendor", "target", "dist", "build",
	".venv", "venv", "__pycache__", ".next", ".cache",
}

// repoMap holds the rendered map and whether it is ready yet.
//
// Built once in the background rather than per turn. Indexing a tree costs a
// round trip per file, which is far too slow to sit in front of a turn, and a
// map is orientation rather than live state: a slightly stale one is worth much
// more than a fresh one that arrives after the model needed it.
type repoMap struct {
	mu    sync.Mutex
	text  string
	ready bool
}

func (m *repoMap) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ready {
		return ""
	}
	return m.text
}

func (m *repoMap) set(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text, m.ready = text, true
}

// fileEntry is one indexed file: where it is and what it declares.
type fileEntry struct {
	path    string
	symbols []string
	score   float64
}

// buildRepoMap indexes every root and renders the map.
//
// Definitions come from the language server, which is what makes them accurate
// across languages without a parser per language. Edges are lexical: a file
// references another if it mentions a name that other file declares. That is an
// approximation and it is the right one here, because the alternative is a
// references request per symbol, which is thousands of round trips to rank a
// list nobody reads past the top of.
func buildRepoMap(ctx context.Context, p *pool, cfg RepoMapConfig) string {
	budget := cfg.TokenBudget
	if budget <= 0 {
		budget = DefaultRepoMapBudget
	}
	maxFiles := cfg.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultRepoMapMaxFiles
	}
	exclude := cfg.Exclude
	if exclude == nil {
		exclude = DefaultRepoMapExclude
	}

	paths, skipped := collectSourceFiles(p, exclude, maxFiles)
	if len(paths) == 0 {
		return ""
	}

	entries := make([]*fileEntry, 0, len(paths))
	for _, path := range paths {
		if ctx.Err() != nil {
			return ""
		}
		syms := declaredSymbols(ctx, p, path)
		if len(syms) == 0 {
			continue
		}
		entries = append(entries, &fileEntry{path: path, symbols: syms})
	}
	if len(entries) == 0 {
		return ""
	}

	rank(entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].score > entries[j].score })
	return render(entries, budget, skipped)
}

// collectSourceFiles walks every root for files a configured server handles.
func collectSourceFiles(p *pool, exclude []string, maxFiles int) (paths []string, skipped int) {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	for _, root := range p.roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return fs.SkipDir
				}
				return nil
			}
			if p.forPath(path) == nil {
				return nil
			}
			if len(paths) >= maxFiles {
				skipped++
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}
	return paths, skipped
}

// declaredSymbols asks the server what a file declares, flattened to names.
func declaredSymbols(ctx context.Context, p *pool, path string) []string {
	c := p.forPath(path)
	if c == nil {
		return nil
	}
	if _, err := c.sync(path); err != nil {
		return nil
	}
	var syms []documentSymbol
	if err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
	}, &syms); err != nil {
		return nil
	}
	var out []string
	var walk func([]documentSymbol)
	walk = func(list []documentSymbol) {
		for _, s := range list {
			if name := undecorate(s.Name); name != "" {
				out = append(out, lastSegment(name))
			} else {
				out = append(out, s.Name)
			}
			walk(s.Children)
		}
	}
	walk(syms)
	return out
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// rank scores files by PageRank over the lexical reference graph.
//
// A ranking is what makes this a map rather than a listing: a tree of every
// file under a budget is list_files truncated at an arbitrary point. What a new
// contributor needs first is what the rest of the code leans on, and that is a
// question about the graph rather than about file size or recency, which are
// the cheaper signals and worse proxies.
func rank(entries []*fileEntry) {
	// Which file declares a name. A name declared in several files is ambiguous
	// and carries no signal about direction, so it is dropped rather than
	// pointed at a guess.
	owner := map[string]int{}
	ambiguous := map[string]bool{}
	for i, e := range entries {
		for _, s := range e.symbols {
			if len(s) < 4 {
				// Short names collide constantly across languages (Get, New,
				// id), so they would wire nearly every file to nearly every
				// other and flatten the ranking into noise.
				continue
			}
			if _, seen := owner[s]; seen {
				ambiguous[s] = true
				continue
			}
			owner[s] = i
		}
	}

	out := make([][]int, len(entries))
	for i, e := range entries {
		body, err := os.ReadFile(e.path)
		if err != nil {
			continue
		}
		text := string(body)
		seen := map[int]bool{}
		for name, j := range owner {
			if i == j || ambiguous[name] || seen[j] {
				continue
			}
			if mentions(text, name) {
				seen[j] = true
				out[i] = append(out[i], j)
			}
		}
	}

	n := len(entries)
	score := make([]float64, n)
	next := make([]float64, n)
	for i := range score {
		score[i] = 1 / float64(n)
	}
	const damping = 0.85
	for iter := 0; iter < 20; iter++ {
		for i := range next {
			next[i] = (1 - damping) / float64(n)
		}
		for i, links := range out {
			if len(links) == 0 {
				// A file that references nothing spreads its weight evenly
				// rather than losing it, which keeps the total stable.
				share := score[i] / float64(n)
				for j := range next {
					next[j] += damping * share
				}
				continue
			}
			share := score[i] / float64(len(links))
			for _, j := range links {
				next[j] += damping * share
			}
		}
		copy(score, next)
	}
	for i, e := range entries {
		e.score = score[i]
	}
}

// mentions reports whether text contains name as a whole identifier, so Get
// does not match Getter and a substring cannot inflate a file's rank.
func mentions(text, name string) bool {
	from := 0
	for {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		i += from
		before := byte(' ')
		if i > 0 {
			before = text[i-1]
		}
		after := byte(' ')
		if end := i + len(name); end < len(text) {
			after = text[end]
		}
		if !identByte(before) && !identByte(after) {
			return true
		}
		from = i + len(name)
	}
}

func identByte(b byte) bool {
	r := rune(b)
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// render writes the ranked map within the budget and says what it left out.
//
// A capped listing that stays quiet about being capped reads as the complete
// contents of the repository, which is the same failure list_files and
// search_files already refuse to make.
func render(entries []*fileEntry, budget, skipped int) string {
	var b strings.Builder
	b.WriteString("## Repository map\n\n")
	b.WriteString("The files the rest of the code leans on most, and what they declare.\n")
	b.WriteString("Ranked, not complete: use list_files and search_files for the rest.\n\n")

	shown := 0
	for _, e := range entries {
		line := fmt.Sprintf("%s\n  %s\n", e.path, strings.Join(capSymbols(e.symbols, 12), ", "))
		if (b.Len()+len(line))/4 > budget {
			break
		}
		b.WriteString(line)
		shown++
	}
	if shown == 0 {
		return ""
	}
	if rest := len(entries) - shown; rest > 0 {
		fmt.Fprintf(&b, "\n%d more file(s) not shown, ranked lower.\n", rest)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "%d file(s) were never indexed: the scan stopped at its file cap.\n", skipped)
	}
	return b.String()
}

func capSymbols(syms []string, max int) []string {
	if len(syms) <= max {
		return syms
	}
	out := append([]string{}, syms[:max]...)
	return append(out, fmt.Sprintf("and %d more", len(syms)-max))
}

// startRepoMap indexes in the background and reports the map when it is ready.
//
// The context is detached from construction on purpose: indexing outlives
// New, and tying it to the caller's context would cancel the map the moment a
// startup context was released.
func startRepoMap(p *pool, cfg RepoMapConfig) *repoMap {
	m := &repoMap{}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), repoMapTimeout)
		defer cancel()
		m.set(buildRepoMap(ctx, p, cfg))
	}()
	return m
}

// repoMapTimeout bounds the indexing pass. A tree too large to index inside it
// yields no map rather than an endless background scan.
const repoMapTimeout = 5 * time.Minute

// writeFile is a thin helper so tests can build a tree without importing the
// file tools, which constraint C4 forbids.
func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }

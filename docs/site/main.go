// Command site builds and serves the mcpkit documentation website.
//
// The site renders markdown source-of-truth files that already live in the
// repository (CONFORMANCE.md, conformance audits, package READMEs, design
// docs). Wrappers under content/ stay thin: each carries page metadata as
// front matter and calls renderMarkdownFile against a path relative to the
// repository root.
//
// Build:  go run . -build              (writes ./dist/docs/)
// Serve:  MCPKIT_DOCS_ENV=dev go run . (live-rebuild + http on :8085)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	s3 "github.com/panyam/s3gen"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"go.abhg.dev/goldmark/anchor"
)

var (
	addr  = flag.String("addr", defaultAddress(), "Address for the local preview server")
	build = flag.Bool("build", false, "Build the site once and exit")
)

// repoRoot points at the mcpkit repository root (two directories up from
// docs/site/). All renderMarkdownFile / includeFile paths resolve under it.
var repoRoot string

func init() {
	cwd, err := filepath.Abs(".")
	if err != nil {
		log.Fatalf("docs/site: cannot resolve cwd: %v", err)
	}
	repoRoot, err = filepath.Abs(filepath.Join(cwd, "..", ".."))
	if err != nil {
		log.Fatalf("docs/site: cannot resolve repo root: %v", err)
	}
}

// safeJoin resolves relPath under repoRoot, rejecting absolute paths and any
// traversal that would escape the repository. Returns an empty string when
// the resolved path falls outside the root or the file is unreadable.
func safeJoin(relPath string) (string, bool) {
	clean := filepath.Clean(relPath)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		log.Printf("docs/site: rejected path %q (absolute or traversal)", relPath)
		return "", false
	}
	full, err := filepath.Abs(filepath.Join(repoRoot, clean))
	if err != nil || !strings.HasPrefix(full, repoRoot) {
		log.Printf("docs/site: rejected path %q (escapes repo root)", relPath)
		return "", false
	}
	return full, true
}

// includeFile returns the raw contents of a repo-root-relative file as
// already-HTML. Use for embedding HTML fragments. For markdown, prefer
// renderMarkdownFile so goldmark produces real headings/tables/code.
func includeFile(relPath string) template.HTML {
	full, ok := safeJoin(relPath)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	return template.HTML(data)
}

// renderMarkdownFile reads a repo-root-relative markdown file and returns
// goldmark-rendered HTML. Empty string on missing file or render error so a
// stale path in a wrapper doesn't take the whole build down.
func renderMarkdownFile(relPath string) template.HTML {
	full, ok := safeJoin(relPath)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		log.Printf("docs/site: renderMarkdownFile: %v", err)
		return ""
	}
	var buf bytes.Buffer
	if err := markdown.Convert(data, &buf); err != nil {
		log.Printf("docs/site: render %s: %v", relPath, err)
		return ""
	}
	srcDir := path.Dir(filepath.ToSlash(filepath.Clean(relPath)))
	out := rewriteRepoLinks(buf.String(), srcDir)
	out = linkifyRepoPaths(out)
	return template.HTML(out)
}

// inlineCodeSpan matches an inline <code>…</code>, capturing any immediately
// preceding <a …> so code that is already a link (markdown link with code
// text) is left alone. goldmark renders block code as <pre><code class=…>
// with nested highlight spans, so [^<]+ (no nested tags) only ever matches
// inline spans, never a code block.
var inlineCodeSpan = regexp.MustCompile(`(<a\b[^>]*>)?<code>([^<]+)</code>`)

// repoPathChars restricts auto-linkify candidates to path-shaped tokens.
var repoPathChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// linkifyRepoPaths turns inline code that names a real repository file
// (e.g. `server/server.go` or `core/tool.go:445`) into a link to its source
// on GitHub — the shape gitfile produces, discovered automatically in
// rendered prose. False positives are guarded by requiring both a directory
// separator AND that the path resolves to an actual file in the checkout, so
// ordinary inline code (`context.Background()`, `--flag`, a bare `Makefile`)
// is never touched. Directories are skipped (too easily confused with prose
// like "the auth/ layer"); already-linked code is skipped.
func linkifyRepoPaths(htmlStr string) string {
	return inlineCodeSpan.ReplaceAllStringFunc(htmlStr, func(match string) string {
		m := inlineCodeSpan.FindStringSubmatch(match)
		if m[1] != "" {
			return match // already inside an <a>
		}
		raw := m[2]
		p, line := raw, ""
		if lm := gitLineSuffix.FindStringSubmatch(raw); lm != nil {
			p, line = lm[1], lm[2]
		}
		if !strings.Contains(p, "/") || !repoPathChars.MatchString(p) {
			return match
		}
		info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(p)))
		if err != nil || info.IsDir() {
			return match // only link real files
		}
		target, display := p, p
		if line != "" {
			target, display = p+"#L"+line, p+":"+line
		}
		return fmt.Sprintf(`<a href="%s"><code>%s</code></a>`,
			template.HTMLEscapeString(repoBlobBase+target), template.HTMLEscapeString(display))
	})
}

// The source-of-truth markdown carries repo-relative links (./other.md,
// ../examples/x/main.go, ../../docs/Y.md) written for GitHub. On the rendered
// site those don't resolve — the site doesn't mirror the repo layout and hosts
// no source files. rewriteRepoLinks fixes each relative href/src at render time:
//   - a doc that has its own site page  -> that page's site URL
//   - any other existing repo file/dir  -> GitHub source URL (raw host for <img>)
//   - a site-only relative link (../guides/, ../examples/) or external/anchor -> left as-is
// See sourceToSite for how the doc→page map is derived.

// repoTreeBase / rawBase mirror repoBlobBase for directory and raw-image links.
var (
	repoRootURL  = strings.TrimSuffix(repoBlobBase, "/blob/main/")
	repoTreeBase = repoRootURL + "/tree/main/"
	rawBase      = strings.Replace(repoRootURL, "github.com", "raw.githubusercontent.com", 1) + "/main/"
)

// sourceToSite maps a repo-relative markdown path (a wrapper's `source:` front
// matter) to the site URL that renders it, so links to a doc that has its own
// page land on-site instead of on GitHub. Built lazily from the content/
// wrappers on first render (after cwd is docs/site).
var (
	s2sOnce sync.Once
	s2sMap  map[string]string
)

var frontMatterSource = regexp.MustCompile(`(?m)^source:\s*"?([^"\n]+?)"?\s*$`)

func sourceToSite() map[string]string {
	s2sOnce.Do(func() {
		m := map[string]string{}
		_ = filepath.WalkDir("content", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			mt := frontMatterSource.FindSubmatch(data)
			if mt == nil {
				return nil
			}
			src := path.Clean(strings.TrimSpace(string(mt[1])))
			rel, _ := filepath.Rel("content", p)
			rel = filepath.ToSlash(rel)
			var sitePath string
			if path.Base(rel) == "index.html" {
				sitePath = path.Dir(rel)
			} else {
				sitePath = strings.TrimSuffix(rel, ".html")
			}
			url := sitePathPrefix + "/"
			if sitePath != "" && sitePath != "." {
				url += sitePath + "/"
			}
			m[src] = url
			return nil
		})
		s2sMap = m
	})
	return s2sMap
}

var hrefOrSrc = regexp.MustCompile(`(href|src)="([^"]*)"`)

func rewriteRepoLinks(html, srcDir string) string {
	if srcDir == "." {
		srcDir = ""
	}
	return hrefOrSrc.ReplaceAllStringFunc(html, func(match string) string {
		mt := hrefOrSrc.FindStringSubmatch(match)
		attr, u := mt[1], mt[2]
		switch {
		case u == "",
			strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "https://"),
			strings.HasPrefix(u, "mailto:"), strings.HasPrefix(u, "//"),
			strings.HasPrefix(u, "#"), strings.HasPrefix(u, "/"),
			strings.HasPrefix(u, "data:"):
			return match
		}
		frag := ""
		if i := strings.IndexAny(u, "#?"); i >= 0 {
			frag, u = u[i:], u[:i]
		}
		if u == "" {
			return match
		}
		target := path.Clean(path.Join(srcDir, u))
		if target == ".." || strings.HasPrefix(target, "../") {
			return match // escapes repo root — leave alone
		}
		// (a) doc with its own site page.
		if site, ok := sourceToSite()[target]; ok {
			return attr + `="` + site + frag + `"`
		}
		// (b) directory whose README has a site page.
		if site, ok := sourceToSite()[path.Join(target, "README.md")]; ok {
			return attr + `="` + site + frag + `"`
		}
		// (c) an existing repo file/dir — point at the real source on GitHub.
		info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(target)))
		if err != nil {
			return match // not a repo path — a site-only relative link; leave it.
		}
		if attr == "src" {
			return attr + `="` + rawBase + target + frag + `"`
		}
		if info.IsDir() {
			return attr + `="` + repoTreeBase + target + frag + `"`
		}
		return attr + `="` + repoBlobBase + target + frag + `"`
	})
}

// sitePathPrefix is the site's URL base. Kept as a const (not read off Site)
// so the link rewriter can reference it without an initialization cycle
// through Site's CommonFuncMap.
const sitePathPrefix = "/mcpkit"

// repoBlobBase is the GitHub blob URL prefix (…/blob/main/), read from
// content/SiteMetadata.json so the repo lives in one place. gitfile builds
// source links against it.
var repoBlobBase = func() string {
	base := "https://github.com/panyam/mcpkit"
	if data, err := os.ReadFile(filepath.Join("content", "SiteMetadata.json")); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if r, ok := m["repository"].(string); ok && r != "" {
				base = r
			}
		}
	}
	return strings.TrimRight(base, "/") + "/blob/main/"
}()

// gitLineSuffix matches a trailing ":<line>" or "#L<line>" reference on a path
// (e.g. server/server.go:541). GitHub uses the #L<line> anchor form.
var gitLineSuffix = regexp.MustCompile(`^(.*?)(?::|#L)(\d+)$`)

// gitfile renders a repository-relative path as a link to its source on GitHub,
// shown as inline <code>. It exists so any filename mentioned in a wrapper (or
// prose we control) becomes a clickable source link instead of dead text.
//
//	{{ gitfile "server/server.go" }}       → <a …/blob/main/server/server.go><code>server/server.go</code></a>
//	{{ gitfile "core/tool.go:445" }}        → links to …/core/tool.go#L445, shown as core/tool.go:445
//
// The path is NOT validated against the working tree (docs/site builds from a
// checkout that may not contain every referenced module), so a typo yields a
// 404 link, not a build failure.
func gitfile(relPath string) template.HTML {
	clean := strings.TrimSpace(relPath)
	if clean == "" {
		return ""
	}
	display := clean
	target := clean
	if m := gitLineSuffix.FindStringSubmatch(clean); m != nil {
		target = m[1] + "#L" + m[2]
		display = m[1] + ":" + m[2]
	}
	href := repoBlobBase + target
	return template.HTML(fmt.Sprintf(`<a href="%s"><code>%s</code></a>`,
		template.HTMLEscapeString(href), template.HTMLEscapeString(display)))
}

var markdown = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Strikethrough,
		extension.Typographer,
		highlighting.NewHighlighting(highlighting.WithStyle("github")),
		&anchor.Extender{},
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
		html.WithXHTML(),
		html.WithUnsafe(),
	),
)

var Site = &s3.Site{
	OutputDir:   "./dist/docs",
	ContentRoot: "./content",
	PathPrefix:  sitePathPrefix,
	TemplateFolders: []string{
		"./templates",
	},
	StaticFolders: []string{
		"./static",
	},
	DefaultBaseTemplate: s3.BaseTemplate{
		Name: "BasePage.html",
		Params: map[any]any{
			"BodyTemplateName": "Content",
		},
	},
	CommonFuncMap: map[string]any{
		"includeFile":        includeFile,
		"renderMarkdownFile": renderMarkdownFile,
		"gitfile":            gitfile,
	},
}

func main() {
	flag.Parse()
	if *build || os.Getenv("MCPKIT_DOCS_ENV") != "production" {
		Site.Rebuild(nil)
	}
	if *build {
		return
	}
	Site.Watch()
	Site.Serve(*addr)
}

func defaultAddress() string {
	if a := os.Getenv("MCPKIT_DOCS_PORT"); a != "" {
		return a
	}
	return ":8085"
}

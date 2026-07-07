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
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	return template.HTML(buf.String())
}

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
	PathPrefix:  "/mcpkit",
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

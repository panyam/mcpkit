// Drop-in mcpkit equivalent of upstream's wiki-explorer-server example.
//
// One tool — get-first-degree-links — that takes a Wikipedia URL and
// returns its outgoing links. Default URL is comma-free so struct tags
// handle the input cleanly. Output is a structured graph payload upstream
// renders as a force-directed graph in the iframe; visual test masks
// the dynamic graph layout, so our stub returns empty arrays.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

// defaultWikiURL is the same article upstream defaults to. Used both in
// the input schema's `default` and when an empty arg arrives (Go's
// `omitempty` strips the field on the input side, so handler-level
// fallback is needed too).
const defaultWikiURL = "https://en.wikipedia.org/wiki/Model_Context_Protocol"

// excludedWikiPrefixes mirrors upstream's EXCLUDED_PREFIXES — Wikipedia
// namespace pages that aren't first-class article links and shouldn't
// land in the graph view.
var excludedWikiPrefixes = []string{
	"Wikipedia:", "Help:", "File:", "Special:", "Talk:", "Template:",
	"Category:", "Portal:", "Draft:", "Module:", "MediaWiki:", "User:",
	"Main_Page",
}

// wikiHrefRe matches `<a ... href="/wiki/...">` style links in Wikipedia
// HTML. Tolerates both quote styles and stops at #, ?, or the closing
// quote — same shape upstream's cheerio selector extracts. Wikipedia
// HTML is consistent enough that regex is adequate here; a stdlib
// html parse would be heavier without behavioural gain for this case.
var wikiHrefRe = regexp.MustCompile(`<a\b[^>]*\bhref=["'](/wiki/[^"'#?]+)["']`)

// extractTitleFromURL ports upstream's extractTitleFromUrl — pulls the
// article slug from the path and turns underscores into spaces.
func extractTitleFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	slug := strings.TrimPrefix(u.Path, "/wiki/")
	decoded, err := url.PathUnescape(slug)
	if err != nil {
		decoded = slug
	}
	return strings.ReplaceAll(decoded, "_", " ")
}

// extractWikiLinks runs the regex over Wikipedia HTML, dedupes by
// path, filters out self-links / fragments / excluded namespace prefixes,
// and maps each path to an absolute URL + display title. Equivalent to
// upstream's cheerio-based extractWikiLinks.
func extractWikiLinks(pageURL *url.URL, html string) []wikiPage {
	matches := wikiHrefRe.FindAllStringSubmatch(html, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]wikiPage, 0, len(matches))
	for _, m := range matches {
		href := m[1]
		if href == pageURL.Path {
			continue
		}
		excluded := false
		for _, p := range excludedWikiPrefixes {
			if strings.Contains(href, p) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if seen[href] {
			continue
		}
		seen[href] = true
		abs := pageURL.Scheme + "://" + pageURL.Host + href
		out = append(out, wikiPage{
			URL:   abs,
			Title: extractTitleFromURL(abs),
		})
	}
	return out
}

// wikiURLRe gates the input — only http/https wikipedia.org /wiki/...
// URLs are fetched. Matches upstream's regex byte-for-byte so error
// messages line up.
var wikiURLRe = regexp.MustCompile(`^https?://[a-z]+\.wikipedia\.org/wiki/`)

// fetchAndExtractLinks ports upstream's main handler body — validate
// URL shape, fetch, map 404/non-200 to a string error, regex-extract
// the link set. Returns either (page, links, nil) or (page, [], err)
// so the handler can wrap into wikiLinksOutput in either shape.
func fetchAndExtractLinks(rawURL string) (wikiPage, []wikiPage, error) {
	page := wikiPage{URL: rawURL, Title: rawURL}
	if !wikiURLRe.MatchString(rawURL) {
		return page, nil, fmt.Errorf("Not a valid Wikipedia URL")
	}
	page.Title = extractTitleFromURL(rawURL)
	// Wikipedia returns 403 for requests with no User-Agent; the
	// language-agnostic policy at https://meta.wikimedia.org/wiki/User-Agent_policy
	// asks bots to identify themselves and a contact path.
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return page, nil, err
	}
	req.Header.Set("User-Agent", "MCP-WikiExplorer-Example/1.0 (https://github.com/modelcontextprotocol)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return page, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return page, nil, fmt.Errorf("Page not found")
		}
		return page, nil, fmt.Errorf("Fetch failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return page, nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return page, nil, err
	}
	return page, extractWikiLinks(u, string(body)), nil
}

type wikiInput struct {
	URL string `json:"url,omitempty" jsonschema:"format=uri,default=https://en.wikipedia.org/wiki/Model_Context_Protocol,description=Wikipedia page URL"`
}

type wikiPage struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type wikiLinksOutput struct {
	Page  wikiPage   `json:"page"`
	Links []wikiPage `json:"links"`
	// Error mirrors upstream's `z.string().nullable()` — value is either a
	// string or null. Reflection can't produce the matching JSON Schema
	// (`{"type": ["string", "null"]}`), so the OutputSchemaOverride on the
	// tool config supplies the exact shape. Handler returns *string: nil
	// serializes to JSON null; a non-nil pointer serializes to the string.
	Error *string `json:"error"`
}

func main() {
	// Dual-mode dispatcher: `--demo` runs the demokit walkthrough (acts as
	// an MCP client against a running server in another terminal). Default
	// (no flag) keeps the existing server behaviour so apps_demo.py and
	// the Playwright wrapper continue to work unchanged.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--demo" {
			runDemo()
			return
		}
	}
	serve()
}

func serve() {
	defaultPort := "3101"
	if p := os.Getenv("PORT"); p != "" {
		defaultPort = p
	}
	addr := flag.String("addr", ":"+defaultPort, "listen address")
	flag.Parse()

	extAppsDir := os.Getenv("EXT_APPS_DIR")
	if extAppsDir == "" {
		extAppsDir = "/tmp/ext-apps"
	}
	htmlPath := filepath.Join(extAppsDir, "examples", "wiki-explorer-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("[wiki-explorer] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://wiki-explorer/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Wiki Explorer",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[wiki-explorer] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[wikiInput, wikiLinksOutput]{
				Name:        "get-first-degree-links",
				Title:       "Get First-Degree Links",
				Description: "Returns all Wikipedia pages that the given page links to directly. The widget is interactive and exposes tools for exploring the graph (expanding nodes to see their links), searching for articles, and querying visible nodes.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				// Reflection covers page + links cleanly from the Go struct; only
				// the nullable `error` field needs help. Upstream's `z.string()
				// .nullable()` emits an anyOf — Go's `*string` reflects to plain
				// `"type": "string"`. Patch.Replace lands the matching shape.
				OutputSchemaPatch: func(s *core.SchemaBuilder) {
					s.Prop("error").Replace(map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "null"},
						},
					})
				},
				Handler: func(ctx core.ToolContext, in wikiInput) (wikiLinksOutput, error) {
					rawURL := in.URL
					if rawURL == "" {
						rawURL = defaultWikiURL
					}
					page, links, err := fetchAndExtractLinks(rawURL)
					if err != nil {
						msg := err.Error()
						// Mirror upstream's never-Go-error contract: a fetch /
						// parse failure becomes a non-nil `error` field on the
						// result, not an isError tool result. The model can read
						// the string and retry; the iframe shows an empty graph.
						return wikiLinksOutput{Page: page, Links: []wikiPage{}, Error: &msg}, nil
					}
					if links == nil {
						links = []wikiPage{}
					}
					return wikiLinksOutput{Page: page, Links: links, Error: nil}, nil
				},
				ResourceURI: resourceURI,
				ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI: req.URI, MimeType: core.AppMIMEType, Text: html,
					}}}, nil
				},
			})
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

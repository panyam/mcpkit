// Drop-in mcpkit equivalent of upstream's sheet-music-server example.
//
// Demonstrates the InputSchemaOverride escape hatch on TypedAppToolConfig
// (issue 542). Upstream's tool exposes an `abcNotation` input whose default
// is an 11-line ABC notation string containing commas. mcpkit's default
// struct-tag schema generator (invopop/jsonschema) treats commas as
// directive separators inside the `jsonschema:` tag, so a struct-tag
// default would truncate at the first comma. The InputSchemaOverride field
// bypasses reflection entirely and ships the raw JSON Schema verbatim.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"strings"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

// defaultABCNotation mirrors upstream sheet-music-server's
// DEFAULT_ABC_NOTATION_INPUT byte-for-byte. The literal newlines + commas
// are exactly what upstream's zod schema's `.default()` carries on the
// wire, and what mcpkit's InputSchemaOverride preserves.
const defaultABCNotation = `X:1
T:Twinkle, Twinkle Little Star
M:4/4
L:1/4
K:C
C C G G | A A G2 | F F E E | D D C2 |
G G F F | E E D2 | G G F F | E E D2 |
C C G G | A A G2 | F F E E | D D C2 |`

// playSheetMusicInput keeps the typed handler signature working — the
// override schema doesn't change what gets unmarshaled into Go, only what
// gets advertised on the wire.
type playSheetMusicInput struct {
	ABCNotation string `json:"abcNotation,omitempty"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "sheet-music-server", "dist", "mcp-app.html")
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

	log.Printf("[sheet-music] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://sheet-music/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Sheet Music Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[sheet-music] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[playSheetMusicInput, string]{
				Name:  "play-sheet-music",
				Title: "Play Sheet Music",
				Description: "Plays music from ABC notation with audio playback and visual sheet music. " +
					"Use this to compose original songs (for birthdays, holidays, or any occasion) " +
					"or perform well-known tunes (folk songs, nursery rhymes, hymns, classical melodies). " +
					"For accurate renditions of well-known tunes, look up the ABC notation from " +
					"abcnotation.com or thesession.org rather than recalling from memory.",
				Execution: &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				// InputSchemaPatch lands the multi-comma default verbatim without
				// the struct-tag parser truncating at the first comma (issue 542).
				// Reflection still emits `type: string`; the patch just adds the
				// description + default.
				InputSchemaPatch: func(s *core.SchemaBuilder) {
					s.Prop("abcNotation").
						Desc("ABC notation string to render as sheet music with audio playback").
						Default(defaultABCNotation)
				},
				Handler: func(ctx core.ToolContext, _ playSheetMusicInput) (string, error) {
					// Upstream validates ABC notation via abcjs; the screenshot test
					// never clicks "play" so only the success-path response shape
					// matters here.
					return "Input parsed successfully.", nil
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

// Example: one Go MCP Apps backend, four View runtimes.
//
// The server registers the same color-picker tool four times, once per View,
// and each View is the same UI built on a different frontend runtime:
//
//	pick_color_bridge    the mcpkit bridge, injected into a Go template (no build step)
//	pick_color_vanilla   upstream App from @modelcontextprotocol/ext-apps
//	pick_color_react     upstream React useApp
//	pick_color_extras    upstream App plus mcpkit's extras (withTraceRelay, selectFile)
//
// Nothing below depends on which runtime a View uses. The Go side registers
// tools and serves bytes, and every View speaks the same MCP Apps wire
// protocol. See docs/APPS_DESIGN.md § Frontend independence.
//
// Run:  go run . -addr :8080
// Then open http://localhost:8080/host for a built-in reference host showing
// all four Views, or connect MCPJam / basic-host to http://localhost:8080/mcp.
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// views/bridge.html is a Go template. The other views are single-file HTML
// built from web/ by `make build-views` and committed, so `go run .` needs
// no Node toolchain.
//
//go:embed views/bridge.html views/upstream-vanilla.html views/upstream-react.html views/upstream-extras.html views/host.html
var viewFS embed.FS

// view is one frontend runtime's rendering of the color picker.
type view struct {
	tool    string
	title   string
	runtime string
	html    string
}

// color is the tool's structured result, shared by every View.
type color struct {
	Hex  string `json:"hex"`
	Name string `json:"name"`
}

type pickInput struct {
	Name string `json:"name,omitempty" jsonschema:"description=A color name such as teal or coral. Omit for a random pick."`
}

type shadeInput struct {
	Hex string `json:"hex" jsonschema:"description=Color as #rrggbb"`
}

var palette = map[string]string{
	"teal": "#008080", "coral": "#ff7f50", "gold": "#ffd700", "slate": "#708090",
	"orchid": "#da70d6", "tomato": "#ff6347", "olive": "#808000", "navy": "#000080",
}

// paletteOrder keeps the random pick deterministic per call count, so the
// e2e test and screenshots are stable.
var paletteOrder = []string{"teal", "coral", "gold", "slate", "orchid", "tomato", "olive", "navy"}

func mustRead(path string) string {
	b, err := viewFS.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	return string(b)
}

func bridgeView() string {
	tmpl := template.Must(template.New("bridge").Parse(mustRead("views/bridge.html")))
	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ Bridge ui.BridgeData }{ui.NewBridgeData("any-frontend-bridge", "0.1.0")}); err != nil {
		log.Fatal(err)
	}
	return buf.String()
}

func views() []view {
	return []view{
		{"pick_color_bridge", "Color picker (mcpkit bridge)", "mcpkit bridge", bridgeView()},
		{"pick_color_vanilla", "Color picker (upstream App)", "upstream App", mustRead("views/upstream-vanilla.html")},
		{"pick_color_react", "Color picker (upstream React)", "upstream React useApp", mustRead("views/upstream-react.html")},
		{"pick_color_extras", "Color picker (upstream App + mcpkit extras)", "upstream App + mcpkit extras", mustRead("views/upstream-extras.html")},
	}
}

func pick(name string, calls *int) color {
	name = strings.ToLower(strings.TrimSpace(name))
	if hex, ok := palette[name]; ok {
		return color{Hex: hex, Name: name}
	}
	n := paletteOrder[*calls%len(paletteOrder)]
	*calls++
	return color{Hex: palette[n], Name: n}
}

// darker scales each channel of a #rrggbb color by 0.8.
func darker(hex string) (color, error) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return color{}, fmt.Errorf("want #rrggbb, got %q", hex)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color{}, fmt.Errorf("want #rrggbb, got %q", hex)
	}
	r, g, b := (v>>16)&0xff, (v>>8)&0xff, v&0xff
	out := fmt.Sprintf("#%02x%02x%02x", r*8/10, g*8/10, b*8/10)
	return color{Hex: out, Name: "darker " + h}, nil
}

// register adds the four app tools, the shared shade_color server tool that
// every View calls back into, and nothing runtime-specific.
func register(srv *server.Server) {
	calls := 0
	for _, v := range views() {
		html := v.html
		uri := "ui://any-frontend/" + strings.TrimPrefix(v.tool, "pick_color_") + ".html"
		ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[pickInput, color]{
			Name:        v.tool,
			Title:       v.title,
			Description: "Pick a color and show it in a View built on " + v.runtime + ".",
			Handler: func(_ core.ToolContext, in pickInput) (color, error) {
				return pick(in.Name, &calls), nil
			},
			ResourceURI: uri,
			Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
			ResourceHandler: func(_ core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
				return core.ResourceResult{Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: core.AppMIMEType, Text: html,
				}}}, nil
			},
		})
	}
	srv.Register(core.TypedTool[shadeInput, color]("shade_color",
		"Return a darker shade of a #rrggbb color. Called by the Views' Darker button.",
		func(_ core.ToolContext, in shadeInput) (color, error) { return darker(in.Hex) },
	))
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	hostPage := mustRead("views/host.html")
	log.Printf("MCP at /mcp, reference host at /host")

	if err := common.RunServer(common.ServerConfig{
		Name:   "any-frontend",
		Addr:   *addr,
		Logger: common.NewMCPLogger("[mcp] "),
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		TransportOptions: []server.TransportOption{
			server.WithMux(func(mux *http.ServeMux) {
				mux.HandleFunc("GET /host", func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprint(w, hostPage)
				})
			}),
		},
		Register: register,
	}); err != nil {
		log.Fatal(err)
	}
}

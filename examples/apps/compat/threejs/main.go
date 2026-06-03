// Drop-in mcpkit equivalent of upstream's threejs-server example.
//
// One tool — show_threejs_scene — with a multi-line JS code default
// (commas everywhere). Struct-tag reflection can't preserve it (invopop
// truncates at the first comma), so InputSchemaOverride supplies the
// schema directly.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
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

// defaultThreeJSCode mirrors upstream's DEFAULT_THREEJS_CODE byte-for-byte.
const defaultThreeJSCode = `const scene = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(75, width / height, 0.1, 1000);
const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
renderer.setSize(width, height);
renderer.setClearColor(0x000000, 0); // Transparent background

const cube = new THREE.Mesh(
  new THREE.BoxGeometry(1, 1, 1),
  new THREE.MeshStandardMaterial({ color: 0x00ff88 })
);
scene.add(cube);

scene.add(new THREE.DirectionalLight(0xffffff, 1));
scene.add(new THREE.AmbientLight(0x404040));

camera.position.z = 3;

function animate() {
  requestAnimationFrame(animate);
  cube.rotation.x += 0.01;
  cube.rotation.y += 0.01;
  renderer.render(scene, camera);
}
animate();`

type showThreeJSInput struct {
	Code   string `json:"code,omitempty"`
	Height int    `json:"height,omitempty"`
}

type showThreeJSOutput struct {
	Success bool `json:"success"`
}

func main() {
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
	htmlPath := filepath.Join(extAppsDir, "examples", "threejs-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[threejs] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "Three.js Server", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://threejs/mcp-app.html"

	// Tool 1: show_threejs_scene — the App tool with its own UI iframe.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[showThreeJSInput, showThreeJSOutput]{
		Name:        "show_threejs_scene",
		Title:       "Show Three.js Scene",
		Description: "Render an interactive 3D scene with custom Three.js code. Supports transparent backgrounds (alpha: true) for seamless host UI integration. Available globals: THREE, OrbitControls, EffectComposer, RenderPass, UnrealBloomPass, canvas, width, height.",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		InputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code": map[string]any{
					"type":        "string",
					"default":     defaultThreeJSCode,
					"description": "JavaScript code to render the 3D scene",
				},
				"height": map[string]any{
					"type":             "integer",
					"exclusiveMinimum": 0,
					// Upstream's zod `.int()` emits Number.MAX_SAFE_INTEGER
					// as the maximum. Mirror it for parity.
					"maximum":     9007199254740991,
					"default":     400,
					"description": "Height in pixels",
				},
			},
			// Neither field is required — both have defaults.
		},
		Handler: func(ctx core.ToolContext, _ showThreeJSInput) (showThreeJSOutput, error) {
			return showThreeJSOutput{Success: true}, nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	// Tool 2: learn_threejs — plain MCP tool returning Three.js documentation.
	// Visual test never invokes it; the actual doc string returned isn't
	// asserted on. Tool definition (name/title/description/empty input
	// schema) is what the drift check compares.
	learnTyped := core.TypedTool[struct{}, string](
		"learn_threejs",
		"Get documentation and examples for using the Three.js View",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "See https://threejs.org for documentation.", nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
	)
	learnTyped.Title = "Learn Three.js"
	srv.RegisterTool(learnTyped.ToolDef, learnTyped.Handler)

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("threejs compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}

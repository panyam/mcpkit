// Drop-in mcpkit equivalent of upstream's shadertoy-server example.
//
// One tool — render-shadertoy — with six input fields. fragmentShader has
// a multi-line GLSL default (with commas) so InputSchemaOverride is
// required. The other five are simple optional strings. Visual test
// masks the WebGL canvas (HOST_MASKS["shadertoy"] = ["#canvas"]).
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

const defaultFragmentShader = `void mainImage(out vec4 fragColor, in vec2 fragCoord) {
    vec2 uv = fragCoord / iResolution.xy;
    fragColor = vec4(uv, 0.5 + 0.5*sin(iTime), 1.0);
}`

// toolDescription mirrors upstream's TOOL_DESCRIPTION byte-for-byte. It's
// a Description string field (not a struct tag), so embedded commas are
// fine; no override needed for this part.
const toolDescription = `Renders a ShaderToy-compatible GLSL fragment shader in real-time using WebGL 2.0.

SHADER FORMAT (ShaderToy conventions):
Use ShaderToy's mainImage entry point - do NOT use generic GLSL conventions.

  void mainImage(out vec4 fragColor, in vec2 fragCoord) {
      vec2 uv = fragCoord / iResolution.xy;
      fragColor = vec4(uv, 0.5 + 0.5*sin(iTime), 1.0);
  }

Do NOT use: void main(), gl_FragColor, gl_FragCoord - these will not work.

AVAILABLE UNIFORMS:
- iResolution (vec3): viewport resolution in pixels
- iTime (float): elapsed time in seconds
- iTimeDelta (float): time since last frame
- iFrame (int): frame counter
- iMouse (vec4): mouse/touch position in pixels (see MOUSE INTERACTION below)
- iDate (vec4): year, month, day, seconds
- iChannel0-3 (sampler2D): buffer inputs for multi-pass shaders

MOUSE INTERACTION:
The iMouse uniform provides interactive mouse/touch input (works on mobile):
- iMouse.xy: Current position while button/touch is held down (frozen on release)
- iMouse.zw: Click start position (positive when down, negative when released)
- iMouse.z > 0: Button is currently pressed
- iMouse.z < 0: Button was released
- iMouse.z == 0: Never clicked

Example - camera control:
  vec2 uv = iMouse.xy / iResolution.xy;  // normalized 0-1

Example - detect click:
  if (iMouse.z > 0.0) { /* button is down */ }

MULTI-PASS RENDERING:
- Use bufferA-D parameters for feedback effects, blur chains, simulations
- BufferA output -> iChannel0, BufferB -> iChannel1, etc.
- Buffers can sample their own previous frame for feedback loops

LIMITATIONS - Do NOT use:
- External textures (generate noise/patterns procedurally)
- Keyboard input (iKeyboard not available)
- Audio/microphone input
- VR features (mainVR not available)

For procedural noise:
  float hash(vec2 p) { return fract(sin(dot(p,vec2(127.1,311.7)))*43758.5453); }

The widget is interactive and exposes tools for updating shader source code and querying compilation status. Compilation errors are sent to model context automatically.`

type renderShadertoyInput struct {
	FragmentShader string `json:"fragmentShader,omitempty"`
	Common         string `json:"common,omitempty"`
	BufferA        string `json:"bufferA,omitempty"`
	BufferB        string `json:"bufferB,omitempty"`
	BufferC        string `json:"bufferC,omitempty"`
	BufferD        string `json:"bufferD,omitempty"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "shadertoy-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[shadertoy] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "ShaderToy Server", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://shadertoy/mcp-app.html"

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[renderShadertoyInput, string]{
		Name:        "render-shadertoy",
		Title:       "ShaderToy Renderer",
		Description: toolDescription,
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		InputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fragmentShader": map[string]any{
					"type":        "string",
					"default":     defaultFragmentShader,
					"description": "Main Image shader - ShaderToy GLSL code",
				},
				"common": map[string]any{
					"type":        "string",
					"description": "Common code shared across all shaders (optional)",
				},
				"bufferA": map[string]any{
					"type":        "string",
					"description": "Buffer A shader code - accessible as iChannel0 (optional)",
				},
				"bufferB": map[string]any{
					"type":        "string",
					"description": "Buffer B shader code - accessible as iChannel1 (optional)",
				},
				"bufferC": map[string]any{
					"type":        "string",
					"description": "Buffer C shader code - accessible as iChannel2 (optional)",
				},
				"bufferD": map[string]any{
					"type":        "string",
					"description": "Buffer D shader code - accessible as iChannel3 (optional)",
				},
			},
		},
		Handler: func(ctx core.ToolContext, _ renderShadertoyInput) (string, error) {
			return "Shader rendered successfully", nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("shadertoy compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}

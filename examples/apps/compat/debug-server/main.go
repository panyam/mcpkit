// Drop-in mcpkit equivalent of upstream's debug-server example.
//
// Three tools — debug-tool (kitchen-sink), debug-refresh (app-only
// polling), debug-log (app-only logging). With the core/schema.go fix
// for `any` reflection landed in issue 548 Gap 2, the debug-log tool's
// `Payload any` field reflects to `{}` directly — no InputSchemaOverride
// needed. Idiomatic Go int types for counters and byte lengths.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

type debugInput struct {
	ContentType              string `json:"contentType,omitempty" jsonschema:"enum=text,enum=image,enum=audio,enum=resource,enum=resourceLink,enum=mixed,default=text"`
	MultipleBlocks           bool   `json:"multipleBlocks,omitempty" jsonschema:"default=true"`
	IncludeStructuredContent bool   `json:"includeStructuredContent,omitempty" jsonschema:"default=true"`
	IncludeMeta              bool   `json:"includeMeta,omitempty" jsonschema:"default=true"`
	LargeInput               string `json:"largeInput,omitempty"`
	SimulateError            bool   `json:"simulateError,omitempty" jsonschema:"default=false"`
	DelayMs                  int    `json:"delayMs,omitempty"`
}

type debugOutput struct {
	Config           map[string]any `json:"config"`
	Timestamp        string         `json:"timestamp"`
	Counter          int64          `json:"counter"`
	LargeInputLength int            `json:"largeInputLength,omitempty"`
}

type debugRefreshOutput struct {
	Timestamp string `json:"timestamp"`
	Counter   int64  `json:"counter"`
}

type debugLogInput struct {
	Type    string `json:"type" jsonschema:"required"`
	Payload any    `json:"payload"`
}

type debugLogOutput struct {
	Logged  bool   `json:"logged"`
	LogFile string `json:"logFile"`
}

// callCounter is a per-process counter incremented on each tool call —
// matches upstream's stateful demo behavior. The visual test doesn't check
// the value; it's here so the response shape stays honest.
var callCounter atomic.Int64

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
	htmlPath := filepath.Join(extAppsDir, "examples", "debug-server", "dist", "mcp-app.html")
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

	log.Printf("[debug-server] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	if err := common.RunServer(common.ServerConfig{
		Name:      "Debug MCP App Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[debug-server] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			registerDebugServerTools(srv, html)
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func registerDebugServerTools(srv *server.Server, html string) {
	resourceURI := "ui://debug-tool/mcp-app.html"

	// Tool 1: debug-tool — the kitchen-sink tool with its own UI resource.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[debugInput, debugOutput]{
		Name:        "debug-tool",
		Title:       "Debug Tool",
		Description: "Comprehensive debug tool for testing MCP Apps SDK. Configure content types, error simulation, delays, and more.",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		Handler: func(ctx core.ToolContext, in debugInput) (debugOutput, error) {
			counter := callCounter.Add(1)
			out := debugOutput{
				Config:    map[string]any{"contentType": in.ContentType},
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
				Counter:   counter,
			}
			if in.LargeInput != "" {
				out.LargeInputLength = len(in.LargeInput)
			}
			return out, nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	// Tool 2: debug-refresh — app-only polling tool, shares the iframe.
	// Resource URI in _meta but visibility=["app"] (model can't see it).
	// Like system-monitor's poll-system-stats, drops to core.TypedTool to
	// avoid double-registering the UI resource RegisterTypedAppTool would
	// otherwise insist on.
	refreshTyped := core.TypedTool[struct{}, debugRefreshOutput](
		"debug-refresh",
		"App-only tool for polling server state. Not visible to the model.",
		func(ctx core.ToolContext, _ struct{}) (debugRefreshOutput, error) {
			return debugRefreshOutput{
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
				Counter:   callCounter.Load(),
			}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: resourceURI,
				Visibility:  []core.UIVisibility{core.UIVisibilityApp},
			},
		}),
	)
	refreshTyped.Title = "Refresh Debug Info"
	srv.RegisterTool(refreshTyped.ToolDef, refreshTyped.Handler)

	// Tool 3: debug-log — app-only logging tool. Same shape as debug-refresh
	// (resourceUri + visibility=["app"] in _meta.ui). The Payload field is
	// `any`; with the core/schema.go reflection fix it lands as `{}` on the
	// wire, matching upstream's `z.unknown()` byte-for-byte without override.
	logTyped := core.TypedTool[debugLogInput, debugLogOutput](
		"debug-log",
		"App-only tool for logging events to the server log file. Not visible to the model.",
		func(ctx core.ToolContext, _ debugLogInput) (debugLogOutput, error) {
			return debugLogOutput{Logged: true, LogFile: "/tmp/mcp-apps-debug-server.log"}, nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: resourceURI,
				Visibility:  []core.UIVisibility{core.UIVisibilityApp},
			},
		}),
	)
	logTyped.Title = "Log to File"
	srv.RegisterTool(logTyped.ToolDef, logTyped.Handler)
}

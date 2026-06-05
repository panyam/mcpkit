// Drop-in mcpkit equivalent of upstream's system-monitor-server example.
//
// Exposes two tools:
//
//   1. get-system-info  (model + app visible): static system info — hostname,
//      platform, CPU model+count, total memory.
//   2. poll-system-stats (app-only visibility): dynamic polling metrics —
//      per-core CPU timing, memory usage, uptime. Upstream uses this from
//      the iframe to drive the live charts.
//
// The second tool has no UI resource of its own; it carries
// _meta.ui.visibility=["app"] but no _meta.ui.resourceUri. RegisterTypedAppTool
// always pairs a tool with a resource, so we drop down to core.TypedTool +
// srv.RegisterTool for that one. (Gap: ext/ui could expose a helper for
// "typed tool with metadata only" — see follow-up issue.)
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"strings"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

// All numeric fields are semantically integers (counts, byte totals,
// whole-number percentages, CPU timer ticks, uptime seconds). Using Go
// `int` / `uint64` emits JSON Schema "type": "integer" — strictly more
// precise than upstream's zod-everywhere "type": "number". The drift
// comparator normalizes the two as equivalent (integer ⊂ number), so the
// idiomatic Go types win.

type systemInfoCPU struct {
	Model string `json:"model"`
	Count int    `json:"count"`
}

type systemInfoMemory struct {
	TotalBytes uint64 `json:"totalBytes"`
}

type systemInfoOutput struct {
	Hostname string           `json:"hostname"`
	Platform string           `json:"platform"`
	Arch     string           `json:"arch"`
	CPU      systemInfoCPU    `json:"cpu"`
	Memory   systemInfoMemory `json:"memory"`
}

type cpuCore struct {
	Idle  int64 `json:"idle"`
	Total int64 `json:"total"`
}

type pollStatsCPU struct {
	Cores []cpuCore `json:"cores"`
}

type pollStatsMemory struct {
	UsedBytes   uint64 `json:"usedBytes"`
	UsedPercent int    `json:"usedPercent"`
	FreeBytes   uint64 `json:"freeBytes"`
}

type pollStatsUptime struct {
	Seconds int64 `json:"seconds"`
}

type pollStatsOutput struct {
	CPU       pollStatsCPU    `json:"cpu"`
	Memory    pollStatsMemory `json:"memory"`
	Uptime    pollStatsUptime `json:"uptime"`
	Timestamp string          `json:"timestamp"`
}

func getSystemInfo() systemInfoOutput {
	host, _ := os.Hostname()
	return systemInfoOutput{
		Hostname: host,
		Platform: runtime.GOOS + " " + runtime.GOARCH,
		Arch:     runtime.GOARCH,
		CPU: systemInfoCPU{
			Model: "Unknown",
			Count: runtime.NumCPU(),
		},
		Memory: systemInfoMemory{
			TotalBytes: 0, // placeholder; visual test doesn't depend on accuracy
		},
	}
}

func getPollStats() pollStatsOutput {
	cores := make([]cpuCore, runtime.NumCPU())
	return pollStatsOutput{
		CPU:       pollStatsCPU{Cores: cores},
		Memory:    pollStatsMemory{UsedBytes: 0, UsedPercent: 0, FreeBytes: 0},
		Uptime:    pollStatsUptime{Seconds: 0},
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
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
	htmlPath := filepath.Join(extAppsDir, "examples", "system-monitor-server", "dist", "mcp-app.html")
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

	log.Printf("[system-monitor] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	if err := common.RunServer(common.ServerConfig{
		Name:      "System Monitor Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[system-monitor] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			registerSystemMonitorTools(srv, html)
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func registerSystemMonitorTools(srv *server.Server, html string) {
	resourceURI := "ui://system-monitor/mcp-app.html"

	// Tool 1: get-system-info — has the UI resource (standard pattern).
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, systemInfoOutput]{
		Name:        "get-system-info",
		Title:       "Get System Info",
		Description: "Returns system information, including hostname, platform, CPU info, and memory.",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		Handler: func(ctx core.ToolContext, _ struct{}) (systemInfoOutput, error) {
			return getSystemInfo(), nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	// Tool 2: poll-system-stats — app-only visibility, NO own resource. The
	// RegisterTypedAppTool helper insists on a paired UI resource, so drop
	// down to core.TypedTool + manual ToolDef mutation. The tool ships with
	// _meta.ui.visibility=["app"] and no resourceUri, matching upstream.
	pollTyped := core.TypedTool[struct{}, pollStatsOutput](
		"poll-system-stats",
		"Returns dynamic system metrics for polling: per-core CPU timing, memory usage, and uptime. App-only.",
		func(ctx core.ToolContext, _ struct{}) (pollStatsOutput, error) {
			return getPollStats(), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				Visibility: []core.UIVisibility{core.UIVisibilityApp},
			},
		}),
	)
	pollTyped.Title = "Poll System Stats"
	srv.RegisterTool(pollTyped.ToolDef, pollTyped.Handler)
}

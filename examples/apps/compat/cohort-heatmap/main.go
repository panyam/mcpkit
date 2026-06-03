// Drop-in mcpkit equivalent of upstream's cohort-heatmap-server example.
//
// One tool — get-cohort-data — with an enum/default-laden input schema and
// a deeply nested output schema. Defaults are single values (no commas), so
// struct-tag reflection works cleanly. Idiomatic Go types throughout —
// counts and indexes are int; Retention is float64 (it's a ratio like
// 0.85). The drift comparator normalizes "integer" ↔ "number" so the
// idiomatic types pass cleanly against upstream's "number"-everywhere
// zod-derived schema.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

type cohortInput struct {
	Metric      string `json:"metric,omitempty" jsonschema:"enum=retention,enum=revenue,enum=active,default=retention"`
	PeriodType  string `json:"periodType,omitempty" jsonschema:"enum=monthly,enum=weekly,default=monthly"`
	CohortCount int    `json:"cohortCount,omitempty" jsonschema:"minimum=3,maximum=24,default=12"`
	MaxPeriods  int    `json:"maxPeriods,omitempty" jsonschema:"minimum=3,maximum=24,default=12"`
}

type cohortCell struct {
	CohortIndex   int     `json:"cohortIndex"`
	PeriodIndex   int     `json:"periodIndex"`
	Retention     float64 `json:"retention"`
	UsersRetained int     `json:"usersRetained"`
	UsersOriginal int     `json:"usersOriginal"`
}

type cohortRow struct {
	CohortID      string       `json:"cohortId"`
	CohortLabel   string       `json:"cohortLabel"`
	OriginalUsers int          `json:"originalUsers"`
	Cells         []cohortCell `json:"cells"`
}

type cohortDataOutput struct {
	Cohorts      []cohortRow `json:"cohorts"`
	Periods      []string    `json:"periods"`
	PeriodLabels []string    `json:"periodLabels"`
	Metric       string      `json:"metric"`
	PeriodType   string      `json:"periodType"`
	GeneratedAt  string      `json:"generatedAt"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "cohort-heatmap-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[cohort-heatmap] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "Cohort Heatmap Server", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://get-cohort-data/mcp-app.html"

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[cohortInput, cohortDataOutput]{
		Name:        "get-cohort-data",
		Title:       "Get Cohort Retention Data",
		Description: "Returns cohort retention heatmap data showing customer retention over time by signup month",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		Handler: func(ctx core.ToolContext, in cohortInput) (cohortDataOutput, error) {
			// Visual test never asserts on the data shape — return a stub
			// matching the declared types. Upstream's iframe generates its
			// own demo data when the tool returns empty arrays.
			return cohortDataOutput{
				Cohorts:      []cohortRow{},
				Periods:      []string{},
				PeriodLabels: []string{},
				Metric:       in.Metric,
				PeriodType:   in.PeriodType,
				GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			}, nil
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

	log.Printf("cohort-heatmap compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}

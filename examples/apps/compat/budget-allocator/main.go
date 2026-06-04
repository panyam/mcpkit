// Drop-in mcpkit equivalent of upstream's budget-allocator-server example.
//
// One tool — get-budget-data — with empty input and a deeply nested output
// (budget config + analytics: history with per-category allocations,
// stage benchmarks with percentile breakdowns). Numerics are float64
// (zod's z.number() is float), counts that are whole stay as int.
// `z.record(string, X)` maps to Go map[string]X via reflection — clean.
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

type budgetCategory struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Color          string  `json:"color"`
	DefaultPercent float64 `json:"defaultPercent"`
}

type historicalMonth struct {
	Month       string             `json:"month"`
	Allocations map[string]float64 `json:"allocations"`
}

type benchmarkPercentiles struct {
	P25 float64 `json:"p25"`
	P50 float64 `json:"p50"`
	P75 float64 `json:"p75"`
}

type stageBenchmark struct {
	Stage              string                          `json:"stage"`
	CategoryBenchmarks map[string]benchmarkPercentiles `json:"categoryBenchmarks"`
}

type budgetConfig struct {
	Categories     []budgetCategory `json:"categories"`
	PresetBudgets  []float64        `json:"presetBudgets"`
	DefaultBudget  float64          `json:"defaultBudget"`
	Currency       string           `json:"currency"`
	CurrencySymbol string           `json:"currencySymbol"`
}

type budgetAnalytics struct {
	History      []historicalMonth `json:"history"`
	Benchmarks   []stageBenchmark  `json:"benchmarks"`
	Stages       []string          `json:"stages"`
	DefaultStage string            `json:"defaultStage"`
}

type budgetDataOutput struct {
	Config    budgetConfig    `json:"config"`
	Analytics budgetAnalytics `json:"analytics"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "budget-allocator-server", "dist", "mcp-app.html")
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

	log.Printf("[budget-allocator] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://budget-allocator/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Budget Allocator Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[budget-allocator] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, budgetDataOutput]{
				Name:        "get-budget-data",
				Title:       "Get Budget Data",
				Description: "Returns budget configuration with 24 months of historical allocations and industry benchmarks by company stage. The widget is interactive and exposes tools for reading/modifying allocations, adjusting budgets, and comparing against industry benchmarks.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, _ struct{}) (budgetDataOutput, error) {
					// Visual test doesn't depend on the data shape; the iframe renders
					// its own demo content. Return an empty-but-typed payload.
					return budgetDataOutput{
						Config: budgetConfig{
							Categories:     []budgetCategory{},
							PresetBudgets:  []float64{},
							Currency:       "USD",
							CurrencySymbol: "$",
						},
						Analytics: budgetAnalytics{
							History:    []historicalMonth{},
							Benchmarks: []stageBenchmark{},
							Stages:     []string{},
						},
					}, nil
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

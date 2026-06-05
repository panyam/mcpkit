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
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

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

// budgetCategoryInternal mirrors upstream's BudgetCategoryInternal: the wire
// fields plus the trendPerMonth used only by generateBudgetHistory. The
// trend never leaves the server; only the wire fields go in the tool
// response. Keeping the split here mirrors upstream's separation in
// server.ts and makes it obvious which fields are internal-only.
type budgetCategoryInternal struct {
	budgetCategory
	TrendPerMonth float64
}

// internalCategories is ported verbatim from upstream
// budget-allocator-server/server.ts (`const CATEGORIES`). Keep this list
// byte-for-byte in sync with upstream — id / color / defaultPercent /
// trendPerMonth all feed into the iframe's rendering and the seeded
// 24-month history.
var internalCategories = []budgetCategoryInternal{
	{budgetCategory{ID: "marketing", Name: "Marketing", Color: "#3b82f6", DefaultPercent: 25}, 0.15},
	{budgetCategory{ID: "engineering", Name: "Engineering", Color: "#10b981", DefaultPercent: 35}, -0.10},
	{budgetCategory{ID: "operations", Name: "Operations", Color: "#f59e0b", DefaultPercent: 15}, 0.05},
	{budgetCategory{ID: "sales", Name: "Sales", Color: "#ef4444", DefaultPercent: 15}, 0.08},
	{budgetCategory{ID: "rd", Name: "R&D", Color: "#8b5cf6", DefaultPercent: 10}, -0.18},
}

// budgetCategoriesWire is the upstream-CATEGORIES projection onto the wire
// (no trendPerMonth). Computed once at init.
var budgetCategoriesWire = func() []budgetCategory {
	out := make([]budgetCategory, len(internalCategories))
	for i, c := range internalCategories {
		out[i] = c.budgetCategory
	}
	return out
}()

// budgetBenchmarks is ported verbatim from upstream's `const BENCHMARKS`.
// Four stages × per-category p25/p50/p75 percentile bands.
var budgetBenchmarks = []stageBenchmark{
	{Stage: "Seed", CategoryBenchmarks: map[string]benchmarkPercentiles{
		"marketing":   {P25: 15, P50: 20, P75: 25},
		"engineering": {P25: 40, P50: 47, P75: 55},
		"operations":  {P25: 8, P50: 12, P75: 15},
		"sales":       {P25: 10, P50: 15, P75: 20},
		"rd":          {P25: 5, P50: 10, P75: 15},
	}},
	{Stage: "Series A", CategoryBenchmarks: map[string]benchmarkPercentiles{
		"marketing":   {P25: 20, P50: 25, P75: 30},
		"engineering": {P25: 35, P50: 40, P75: 45},
		"operations":  {P25: 10, P50: 14, P75: 18},
		"sales":       {P25: 15, P50: 20, P75: 25},
		"rd":          {P25: 8, P50: 12, P75: 15},
	}},
	{Stage: "Series B", CategoryBenchmarks: map[string]benchmarkPercentiles{
		"marketing":   {P25: 22, P50: 27, P75: 32},
		"engineering": {P25: 30, P50: 35, P75: 40},
		"operations":  {P25: 12, P50: 16, P75: 20},
		"sales":       {P25: 18, P50: 23, P75: 28},
		"rd":          {P25: 8, P50: 12, P75: 15},
	}},
	{Stage: "Growth", CategoryBenchmarks: map[string]benchmarkPercentiles{
		"marketing":   {P25: 25, P50: 30, P75: 35},
		"engineering": {P25: 25, P50: 30, P75: 35},
		"operations":  {P25: 15, P50: 18, P75: 22},
		"sales":       {P25: 20, P50: 25, P75: 30},
		"rd":          {P25: 5, P50: 8, P75: 12},
	}},
}

// seededRandom mirrors upstream's LCG in server.ts (seeded with 42):
//   seed = (seed * 1103515245 + 12345) & 0x7fffffff
//   return seed / 0x7fffffff
// Faithful port so the generated history matches upstream's TS output.
// Closure-style to match the JS return-a-function shape.
func seededRandom(seed int64) func() float64 {
	state := seed
	const mask = int64(0x7fffffff)
	return func() float64 {
		state = (state*1103515245 + 12345) & mask
		return float64(state) / float64(mask)
	}
}

// generateBudgetHistory produces 24 months of allocation history with the
// same trend + noise + normalize-to-100 + round-to-0.1 algorithm as
// upstream's generateHistory(). The fixed seed (42) guarantees the
// rendered chart matches what upstream produces.
func generateBudgetHistory() []historicalMonth {
	const months = 24
	rand := seededRandom(42)
	now := time.Now()
	out := make([]historicalMonth, 0, months)
	for i := months - 1; i >= 0; i-- {
		// JS: new Date(now); setMonth(getMonth() - i) -- month rollover is
		// automatic; Go's AddDate normalizes the same way.
		d := now.AddDate(0, -i, 0)
		monthStr := fmt.Sprintf("%04d-%02d", d.Year(), int(d.Month()))

		raw := make(map[string]float64, len(internalCategories))
		monthsFromStart := float64(months - 1 - i)
		for _, cat := range internalCategories {
			trend := monthsFromStart * cat.TrendPerMonth
			noise := (rand() - 0.5) * 3 // +/- 1.5%
			v := cat.DefaultPercent + trend + noise
			if v < 0 {
				v = 0
			} else if v > 100 {
				v = 100
			}
			raw[cat.ID] = v
		}
		// Normalize to 100% and round to 0.1.
		total := 0.0
		for _, v := range raw {
			total += v
		}
		allocations := make(map[string]float64, len(raw))
		for id, v := range raw {
			allocations[id] = math.Round(v/total*1000) / 10
		}
		out = append(out, historicalMonth{Month: monthStr, Allocations: allocations})
	}
	return out
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
					// Real data ported verbatim from upstream's get-budget-data
					// handler (server.ts ~line 268). The iframe SPA renders
					// from this payload — categories drive the legend/chart
					// colors, history populates the trend chart, benchmarks
					// drive the percentile bands. Empty arrays leave the
					// widget stuck on a loading state.
					return budgetDataOutput{
						Config: budgetConfig{
							Categories:     budgetCategoriesWire,
							PresetBudgets:  []float64{50000, 100000, 250000, 500000},
							DefaultBudget:  100000,
							Currency:       "USD",
							CurrencySymbol: "$",
						},
						Analytics: budgetAnalytics{
							History:      generateBudgetHistory(),
							Benchmarks:   budgetBenchmarks,
							Stages:       []string{"Seed", "Series A", "Series B", "Growth"},
							DefaultStage: "Series A",
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

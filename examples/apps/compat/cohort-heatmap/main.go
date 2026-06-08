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
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
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

// retentionParams shape the exponential-decay curve generateRetention
// uses to fabricate one cohort's retention values. Ported verbatim from
// upstream cohort-heatmap-server/server.ts's RetentionParams interface.
type retentionParams struct {
	BaseRetention float64
	DecayRate     float64
	Floor         float64
	Noise         float64
}

// cohortParamsMap mirrors upstream's `paramsMap` keyed on the metric arg —
// retention/revenue/active each get different curve shapes so a viewer
// flipping the dropdown sees the heatmap visibly change. Numbers are
// byte-for-byte the same as upstream.
var cohortParamsMap = map[string]retentionParams{
	"retention": {BaseRetention: 0.75, DecayRate: 0.12, Floor: 0.08, Noise: 0.04},
	"revenue":   {BaseRetention: 0.70, DecayRate: 0.10, Floor: 0.15, Noise: 0.06},
	"active":    {BaseRetention: 0.60, DecayRate: 0.18, Floor: 0.05, Noise: 0.05},
}

// generateRetention is the per-cell retention generator. Exponential
// decay from BaseRetention with a per-cell uniform noise term in
// [-Noise, +Noise], clamped to [0, 1]. Period 0 returns 1.0 (every user
// is retained at signup). Mirrors upstream's `generateRetention()` in
// server.ts.
func generateRetention(period int, p retentionParams, rng *rand.Rand) float64 {
	if period == 0 {
		return 1.0
	}
	base := p.BaseRetention*math.Exp(-p.DecayRate*float64(period-1)) + p.Floor
	variation := (rng.Float64() - 0.5) * 2 * p.Noise
	v := base + variation
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// generateCohortData produces the full structured payload the iframe
// renders into a heatmap. Ported verbatim from upstream's
// `generateCohortData` (cohort-heatmap-server/server.ts) with one
// deliberate Go-side change: the RNG is seeded (PCG with seed 42) so the
// fixture's visual baseline stays stable across runs. Upstream's TS
// version uses raw Math.random() which is non-deterministic; that's
// fine for an interactive demo but our committed snapshot test under
// __snapshots__/ needs a stable layout. Determinism only matters for
// the test gate; runtime behavior is identical.
func generateCohortData(metric, periodType string, cohortCount, maxPeriods int) cohortDataOutput {
	rng := rand.New(rand.NewPCG(42, 42))

	params, ok := cohortParamsMap[metric]
	if !ok {
		params = cohortParamsMap["retention"]
	}

	periods := make([]string, 0, maxPeriods)
	periodLabels := make([]string, 0, maxPeriods)
	for i := 0; i < maxPeriods; i++ {
		periods = append(periods, fmt.Sprintf("M%d", i))
		if i == 0 {
			periodLabels = append(periodLabels, "Month 0")
		} else {
			periodLabels = append(periodLabels, fmt.Sprintf("Month %d", i))
		}
	}

	now := time.Now()
	cohorts := make([]cohortRow, 0, cohortCount)
	for c := 0; c < cohortCount; c++ {
		// Oldest first: cohort 0 is `cohortCount-1` months ago.
		cohortDate := now.AddDate(0, -(cohortCount - 1 - c), 0)
		cohortID := cohortDate.Format("2006-01")
		cohortLabel := cohortDate.Format("Jan 2006")

		// Random cohort size 1000-5000 users — same range upstream uses.
		originalUsers := 1000 + rng.IntN(4000)

		// Newer cohorts have fewer months of data — they only started
		// existing partway through the observation window. Same shape as
		// upstream's `periodsAvailable = cohortCount - c`.
		periodsAvailable := cohortCount - c
		maxP := periodsAvailable
		if maxPeriods < maxP {
			maxP = maxPeriods
		}

		cells := make([]cohortCell, 0, maxP)
		previousRetention := 1.0
		for p := 0; p < maxP; p++ {
			retention := generateRetention(p, params, rng)
			// Retention must be monotonically non-increasing except for a small
			// 2pp noise tolerance — mirrors upstream's `Math.min(retention,
			// previousRetention + 0.02)` clamp.
			if retention > previousRetention+0.02 {
				retention = previousRetention + 0.02
			}
			previousRetention = retention
			cells = append(cells, cohortCell{
				CohortIndex:   c,
				PeriodIndex:   p,
				Retention:     retention,
				UsersRetained: int(math.Round(float64(originalUsers) * retention)),
				UsersOriginal: originalUsers,
			})
		}

		cohorts = append(cohorts, cohortRow{
			CohortID:      cohortID,
			CohortLabel:   cohortLabel,
			OriginalUsers: originalUsers,
			Cells:         cells,
		})
	}

	return cohortDataOutput{
		Cohorts:      cohorts,
		Periods:      periods,
		PeriodLabels: periodLabels,
		Metric:       metric,
		PeriodType:   periodType,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// formatCohortSummary mirrors upstream's text-content summary so the
// non-Apps-host view (e.g. MCPJam Inspector, raw curl) shows something
// meaningful. The iframe ignores content[0].text and reads
// structuredContent directly.
func formatCohortSummary(d cohortDataOutput) string {
	var sum float64
	var count int
	for _, c := range d.Cohorts {
		for _, cell := range c.Cells {
			if cell.PeriodIndex > 0 {
				sum += cell.Retention
				count++
			}
		}
	}
	avg := 0.0
	if count > 0 {
		avg = sum / float64(count)
	}
	return fmt.Sprintf(
		"Cohort Analysis: %d cohorts, %d periods\nAverage retention: %.1f%%\nMetric: %s, Period: %s",
		len(d.Cohorts), len(d.Periods), avg*100, d.Metric, d.PeriodType,
	)
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
	htmlPath := filepath.Join(extAppsDir, "examples", "cohort-heatmap-server", "dist", "mcp-app.html")
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

	log.Printf("[cohort-heatmap] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://get-cohort-data/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Cohort Heatmap Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[cohort-heatmap] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[cohortInput, cohortDataOutput]{
				Name:        "get-cohort-data",
				Title:       "Get Cohort Retention Data",
				Description: "Returns cohort retention heatmap data showing customer retention over time by signup month",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, in cohortInput) (cohortDataOutput, error) {
					// Struct-tag defaults shape the input SCHEMA but don't fill
					// zero-value fields at unmarshal time — apply them here so
					// the iframe's "no args" path gets the same response as a
					// fully-specified call.
					metric := in.Metric
					if metric == "" {
						metric = "retention"
					}
					periodType := in.PeriodType
					if periodType == "" {
						periodType = "monthly"
					}
					cohortCount := in.CohortCount
					if cohortCount == 0 {
						cohortCount = 12
					}
					maxPeriods := in.MaxPeriods
					if maxPeriods == 0 {
						maxPeriods = 12
					}
					return generateCohortData(metric, periodType, cohortCount, maxPeriods), nil
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

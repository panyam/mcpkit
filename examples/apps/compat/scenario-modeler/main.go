// Drop-in mcpkit equivalent of upstream's scenario-modeler-server example.
//
// One tool — get-scenario-data — with optional ScenarioInputs input and
// rich nested output (templates array, default inputs, optional custom
// projections + summary). ScenarioSummary has `breakEvenMonth: z.number().
// nullable()` so we use OutputSchemaOverride to mirror the anyOf shape.
// The handler now ports upstream's 5 pre-built scenario templates +
// 12-month projection math so the iframe's "Compare to..." dropdown
// has real options and Reset lands on real defaults rather than zeros.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

type scenarioInputs struct {
	StartingMRR       float64 `json:"startingMRR"`
	MonthlyGrowthRate float64 `json:"monthlyGrowthRate"`
	MonthlyChurnRate  float64 `json:"monthlyChurnRate"`
	GrossMargin       float64 `json:"grossMargin"`
	FixedCosts        float64 `json:"fixedCosts"`
}

type monthlyProjection struct {
	Month             float64 `json:"month"`
	MRR               float64 `json:"mrr"`
	GrossProfit       float64 `json:"grossProfit"`
	NetProfit         float64 `json:"netProfit"`
	CumulativeRevenue float64 `json:"cumulativeRevenue"`
}

type scenarioSummary struct {
	EndingMRR      float64  `json:"endingMRR"`
	ARR            float64  `json:"arr"`
	TotalRevenue   float64  `json:"totalRevenue"`
	TotalProfit    float64  `json:"totalProfit"`
	MRRGrowthPct   float64  `json:"mrrGrowthPct"`
	AvgMargin      float64  `json:"avgMargin"`
	BreakEvenMonth *float64 `json:"breakEvenMonth"` // nullable in upstream
}

type scenarioTemplate struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Icon        string              `json:"icon"`
	Parameters  scenarioInputs      `json:"parameters"`
	Projections []monthlyProjection `json:"projections"`
	Summary     scenarioSummary     `json:"summary"`
	KeyInsight  string              `json:"keyInsight"`
}

type getScenarioInput struct {
	CustomInputs *scenarioInputs `json:"customInputs,omitempty"`
}

type getScenarioOutput struct {
	Templates         []scenarioTemplate  `json:"templates"`
	DefaultInputs     scenarioInputs      `json:"defaultInputs"`
	CustomProjections []monthlyProjection `json:"customProjections,omitempty"`
	CustomSummary     *scenarioSummary    `json:"customSummary,omitempty"`
}

// scenarioInputsSchema is the inline JSON Schema shape for ScenarioInputs.
// Used in the OutputSchemaOverride map below (and inside summary/template).
var scenarioInputsSchemaMap = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"startingMRR":       map[string]any{"type": "number"},
		"monthlyGrowthRate": map[string]any{"type": "number"},
		"monthlyChurnRate":  map[string]any{"type": "number"},
		"grossMargin":       map[string]any{"type": "number"},
		"fixedCosts":        map[string]any{"type": "number"},
	},
	"required": []string{"startingMRR", "monthlyGrowthRate", "monthlyChurnRate", "grossMargin", "fixedCosts"},
}

var monthlyProjectionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"month":             map[string]any{"type": "number"},
		"mrr":               map[string]any{"type": "number"},
		"grossProfit":       map[string]any{"type": "number"},
		"netProfit":         map[string]any{"type": "number"},
		"cumulativeRevenue": map[string]any{"type": "number"},
	},
	"required": []string{"month", "mrr", "grossProfit", "netProfit", "cumulativeRevenue"},
}

// scenarioSummarySchema with anyOf for the nullable breakEvenMonth.
var scenarioSummarySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"endingMRR":    map[string]any{"type": "number"},
		"arr":          map[string]any{"type": "number"},
		"totalRevenue": map[string]any{"type": "number"},
		"totalProfit":  map[string]any{"type": "number"},
		"mrrGrowthPct": map[string]any{"type": "number"},
		"avgMargin":    map[string]any{"type": "number"},
		"breakEvenMonth": map[string]any{
			"anyOf": []any{
				map[string]any{"type": "number"},
				map[string]any{"type": "null"},
			},
		},
	},
	"required": []string{"endingMRR", "arr", "totalRevenue", "totalProfit", "mrrGrowthPct", "avgMargin", "breakEvenMonth"},
}

// defaultInputs mirrors upstream's DEFAULT_INPUTS — what the iframe's
// Reset button lands on. Values match upstream byte-for-byte.
var defaultInputs = scenarioInputs{
	StartingMRR:       50000,
	MonthlyGrowthRate: 5,
	MonthlyChurnRate:  3,
	GrossMargin:       80,
	FixedCosts:        30000,
}

// calculateProjections ports upstream's per-month MRR / gross profit /
// net profit / cumulative-revenue formula verbatim. Net growth =
// growth - churn, MRR compounds, gross profit applies margin, net
// profit subtracts fixed costs.
func calculateProjections(in scenarioInputs) []monthlyProjection {
	netGrowthRate := (in.MonthlyGrowthRate - in.MonthlyChurnRate) / 100.0
	out := make([]monthlyProjection, 0, 12)
	cumulative := 0.0
	for m := 1; m <= 12; m++ {
		mrr := in.StartingMRR * math.Pow(1+netGrowthRate, float64(m))
		grossProfit := mrr * (in.GrossMargin / 100.0)
		netProfit := grossProfit - in.FixedCosts
		cumulative += mrr
		out = append(out, monthlyProjection{
			Month:             float64(m),
			MRR:               mrr,
			GrossProfit:       grossProfit,
			NetProfit:         netProfit,
			CumulativeRevenue: cumulative,
		})
	}
	return out
}

// calculateSummary collapses the 12 monthly projections into the summary
// card the iframe renders bottom-right. breakEvenMonth is *float64 so it
// can serialize as `null` when net profit never crosses zero —
// matches upstream's `z.number().nullable()`.
func calculateSummary(projs []monthlyProjection, in scenarioInputs) scenarioSummary {
	ending := projs[11].MRR
	totalRevenue := 0.0
	totalProfit := 0.0
	for _, p := range projs {
		totalRevenue += p.MRR
		totalProfit += p.NetProfit
	}
	mrrGrowthPct := ((ending - in.StartingMRR) / in.StartingMRR) * 100.0
	avgMargin := 0.0
	if totalRevenue != 0 {
		avgMargin = (totalProfit / totalRevenue) * 100.0
	}
	var breakEven *float64
	for _, p := range projs {
		if p.NetProfit >= 0 {
			m := p.Month
			breakEven = &m
			break
		}
	}
	return scenarioSummary{
		EndingMRR:      ending,
		ARR:            ending * 12,
		TotalRevenue:   totalRevenue,
		TotalProfit:    totalProfit,
		MRRGrowthPct:   mrrGrowthPct,
		AvgMargin:      avgMargin,
		BreakEvenMonth: breakEven,
	}
}

// buildTemplate is the per-template constructor — calls calculate*
// once at startup time so the templates returned to the iframe have
// their projections + summary already baked in.
func buildTemplate(id, name, desc, icon string, params scenarioInputs, keyInsight string) scenarioTemplate {
	projs := calculateProjections(params)
	return scenarioTemplate{
		ID:          id,
		Name:        name,
		Description: desc,
		Icon:        icon,
		Parameters:  params,
		Projections: projs,
		Summary:     calculateSummary(projs, params),
		KeyInsight:  keyInsight,
	}
}

// scenarioTemplates ports upstream's SCENARIO_TEMPLATES list verbatim —
// 5 pre-built scenarios for the iframe's "Compare to..." dropdown.
// Parameters, icons, and key insights match upstream byte-for-byte.
var scenarioTemplates = []scenarioTemplate{
	buildTemplate("bootstrapped", "Bootstrapped Growth",
		"Low burn, steady growth, path to profitability", "\U0001F331",
		scenarioInputs{StartingMRR: 30000, MonthlyGrowthRate: 4, MonthlyChurnRate: 2, GrossMargin: 85, FixedCosts: 20000},
		"Profitable by month 1, but slower scale"),
	buildTemplate("vc-rocketship", "VC Rocketship",
		"High burn, explosive growth, raise more later", "\U0001F680",
		scenarioInputs{StartingMRR: 100000, MonthlyGrowthRate: 15, MonthlyChurnRate: 5, GrossMargin: 70, FixedCosts: 150000},
		"Loses money early but ends at 3x MRR"),
	buildTemplate("cash-cow", "Cash Cow",
		"Mature product, high margin, stable revenue", "\U0001F404",
		scenarioInputs{StartingMRR: 80000, MonthlyGrowthRate: 2, MonthlyChurnRate: 1, GrossMargin: 90, FixedCosts: 40000},
		"Consistent profitability, low risk"),
	buildTemplate("turnaround", "Turnaround",
		"Fighting churn, rebuilding product-market fit", "\U0001F504",
		scenarioInputs{StartingMRR: 60000, MonthlyGrowthRate: 6, MonthlyChurnRate: 8, GrossMargin: 75, FixedCosts: 50000},
		"Negative net growth requires urgent action"),
	buildTemplate("efficient-growth", "Efficient Growth",
		"Balanced approach with sustainable economics", "⚖️",
		scenarioInputs{StartingMRR: 50000, MonthlyGrowthRate: 8, MonthlyChurnRate: 3, GrossMargin: 80, FixedCosts: 35000},
		"Good growth with path to profitability"),
}

var scenarioTemplateSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"id":          map[string]any{"type": "string"},
		"name":        map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"icon":        map[string]any{"type": "string"},
		"parameters":  scenarioInputsSchemaMap,
		"projections": map[string]any{
			"type":  "array",
			"items": monthlyProjectionSchema,
		},
		"summary":    scenarioSummarySchema,
		"keyInsight": map[string]any{"type": "string"},
	},
	"required": []string{"id", "name", "description", "icon", "parameters", "projections", "summary", "keyInsight"},
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
	htmlPath := filepath.Join(extAppsDir, "examples", "scenario-modeler-server", "dist", "mcp-app.html")
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

	log.Printf("[scenario-modeler] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://scenario-modeler/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "SaaS Scenario Modeler",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[scenario-modeler] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[getScenarioInput, getScenarioOutput]{
				Name:        "get-scenario-data",
				Title:       "Get Scenario Data",
				Description: "Returns SaaS scenario templates and optionally computes custom projections for given inputs",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				// InputSchemaPatch just adds the description on `customInputs` —
				// reflection of `*scenarioInputs` already produces the matching
				// nested object shape.
				InputSchemaPatch: func(s *core.SchemaBuilder) {
					s.Prop("customInputs").Desc("Custom scenario parameters to compute projections for")
				},
				// OutputSchemaOverride stays: the nullable `breakEvenMonth` appears
				// at two nesting depths (templates[].summary.breakEvenMonth and
				// customSummary.breakEvenMonth) via the same scenarioSummarySchema
				// map. Hand-stitching that with Patch.Replace at deep paths is
				// uglier than the explicit override.
				OutputSchemaOverride: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"templates": map[string]any{
							"type":  "array",
							"items": scenarioTemplateSchema,
						},
						"defaultInputs": scenarioInputsSchemaMap,
						"customProjections": map[string]any{
							"type":  "array",
							"items": monthlyProjectionSchema,
						},
						"customSummary": scenarioSummarySchema,
					},
					"required": []string{"templates", "defaultInputs"},
				},
				Handler: func(ctx core.ToolContext, in getScenarioInput) (getScenarioOutput, error) {
					out := getScenarioOutput{
						Templates:     scenarioTemplates,
						DefaultInputs: defaultInputs,
					}
					if in.CustomInputs != nil {
						projs := calculateProjections(*in.CustomInputs)
						sum := calculateSummary(projs, *in.CustomInputs)
						out.CustomProjections = projs
						out.CustomSummary = &sum
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
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

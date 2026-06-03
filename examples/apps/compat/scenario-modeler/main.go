// Drop-in mcpkit equivalent of upstream's scenario-modeler-server example.
//
// One tool — get-scenario-data — with optional ScenarioInputs input and
// rich nested output (templates array, default inputs, optional custom
// projections + summary). ScenarioSummary has `breakEvenMonth: z.number().
// nullable()` so we use OutputSchemaOverride to mirror the anyOf shape.
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

	opts := common.MCPServerOptions(*addr, "[scenario-modeler] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "SaaS Scenario Modeler", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://scenario-modeler/mcp-app.html"

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[getScenarioInput, getScenarioOutput]{
		Name:        "get-scenario-data",
		Title:       "Get Scenario Data",
		Description: "Returns SaaS scenario templates and optionally computes custom projections for given inputs",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		// InputSchemaOverride: customInputs is described with text from a
		// .describe() call — no commas in it, so reflection would technically
		// work. But the nested object structure is reused (ScenarioInputs
		// appears in input + output + templates) and being explicit here
		// matches upstream's shape byte-for-byte.
		InputSchemaOverride: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"customInputs": func() map[string]any {
					m := map[string]any{}
					for k, v := range scenarioInputsSchemaMap {
						m[k] = v
					}
					m["description"] = "Custom scenario parameters to compute projections for"
					return m
				}(),
			},
		},
		// OutputSchemaOverride: needed for the nullable breakEvenMonth field
		// (z.number().nullable() emits anyOf, Go reflection emits "number").
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
		Handler: func(ctx core.ToolContext, _ getScenarioInput) (getScenarioOutput, error) {
			// Visual test doesn't depend on the data shape.
			return getScenarioOutput{
				Templates:     []scenarioTemplate{},
				DefaultInputs: scenarioInputs{},
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

	log.Printf("scenario-modeler compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}

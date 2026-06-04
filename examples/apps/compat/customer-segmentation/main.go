// Drop-in mcpkit equivalent of upstream's customer-segmentation-server example.
//
// Single get-customer-data tool with an optional enum input ("All" + the four
// SEGMENTS) and a structured output of customers + segment summaries. No
// commas in the input description, so struct tags work. Idiomatic Go types:
// counts and dollar/score figures are int; EngagementScore stays float64
// (it's typically a 0-100 fractional). Visual test is a random scatter
// plot upstream masks at the host level — our stub returns empty arrays;
// the iframe generates its own demo data.
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

type customerInput struct {
	Segment string `json:"segment,omitempty" jsonschema:"enum=All,enum=Enterprise,enum=Mid-Market,enum=SMB,enum=Startup,description=Filter by segment (default: All)"`
}

type customer struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Segment         string  `json:"segment"`
	AnnualRevenue   int     `json:"annualRevenue"`
	EmployeeCount   int     `json:"employeeCount"`
	AccountAge      int     `json:"accountAge"`
	EngagementScore float64 `json:"engagementScore"`
	SupportTickets  int     `json:"supportTickets"`
	NPS             int     `json:"nps"`
}

type segmentSummary struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Color string `json:"color"`
}

type customerDataOutput struct {
	Customers []customer       `json:"customers"`
	Segments  []segmentSummary `json:"segments"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "customer-segmentation-server", "dist", "mcp-app.html")
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

	log.Printf("[customer-segmentation] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://customer-segmentation/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Customer Segmentation Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[customer-segmentation] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[customerInput, customerDataOutput]{
				Name:        "get-customer-data",
				Title:       "Get Customer Data",
				Description: "Returns customer data with segment information for visualization. Optionally filter by segment.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, in customerInput) (customerDataOutput, error) {
					return customerDataOutput{
						Customers: []customer{},
						Segments:  []segmentSummary{},
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

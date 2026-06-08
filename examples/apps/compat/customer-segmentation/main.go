// Drop-in mcpkit equivalent of upstream's customer-segmentation-server example.
//
// Single get-customer-data tool with an optional enum input ("All" + the four
// SEGMENTS) and a structured output of customers + segment summaries. No
// commas in the input description, so struct tags work. Idiomatic Go types:
// counts and dollar/score figures are int; EngagementScore stays float64
// (it's typically a 0-100 fractional). The handler now ports upstream's
// clustered-Gaussian customer generator so the iframe scatter plot renders
// with the same shape as upstream — seeded via PCG(42, 42) for stable
// visual baselines.
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
	"sync"

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

// segments is the ordered enum used in both the input schema and the
// generated segmentSummary list. Order matches upstream's SEGMENTS const.
var segments = []string{"Enterprise", "Mid-Market", "SMB", "Startup"}

// segmentColors mirrors upstream's SEGMENT_COLORS map — the iframe paints
// each scatter point using these hex codes.
var segmentColors = map[string]string{
	"Enterprise": "#1e40af",
	"Mid-Market": "#0d9488",
	"SMB":        "#059669",
	"Startup":    "#6366f1",
}

// segmentWeights mirrors upstream's SEGMENT_WEIGHTS — selectSegment picks
// a bucket by cumulative weight so the overall distribution stays SMB-heavy.
var segmentWeights = map[string]float64{
	"Enterprise": 0.15,
	"Mid-Market": 0.25,
	"SMB":        0.35,
	"Startup":    0.25,
}

// clusterRange is one inclusive numeric range used to draw a clustered
// Gaussian value for one (segment, metric) cell.
type clusterRange struct{ Min, Max float64 }

// clusterCenter mirrors upstream's CLUSTER_CENTERS entry — six metric
// ranges that shape the Gaussian distribution for one segment.
type clusterCenter struct {
	AnnualRevenue, EmployeeCount, AccountAge clusterRange
	EngagementScore, SupportTickets, NPS     clusterRange
}

// clusterCenters mirrors upstream's CLUSTER_CENTERS table verbatim — these
// numbers shape what a "typical Enterprise" vs "typical Startup" customer
// looks like across the six metrics the scatter plot can axis on.
var clusterCenters = map[string]clusterCenter{
	"Enterprise": {
		AnnualRevenue:   clusterRange{2_000_000, 10_000_000},
		EmployeeCount:   clusterRange{500, 5000},
		AccountAge:      clusterRange{60, 120},
		EngagementScore: clusterRange{70, 95},
		SupportTickets:  clusterRange{5, 20},
		NPS:             clusterRange{40, 80},
	},
	"Mid-Market": {
		AnnualRevenue:   clusterRange{500_000, 2_000_000},
		EmployeeCount:   clusterRange{100, 500},
		AccountAge:      clusterRange{36, 84},
		EngagementScore: clusterRange{60, 85},
		SupportTickets:  clusterRange{10, 30},
		NPS:             clusterRange{20, 60},
	},
	"SMB": {
		AnnualRevenue:   clusterRange{50_000, 500_000},
		EmployeeCount:   clusterRange{10, 100},
		AccountAge:      clusterRange{12, 48},
		EngagementScore: clusterRange{40, 70},
		SupportTickets:  clusterRange{15, 40},
		NPS:             clusterRange{0, 40},
	},
	"Startup": {
		AnnualRevenue:   clusterRange{10_000, 200_000},
		EmployeeCount:   clusterRange{1, 50},
		AccountAge:      clusterRange{1, 24},
		EngagementScore: clusterRange{50, 90},
		SupportTickets:  clusterRange{5, 25},
		NPS:             clusterRange{10, 70},
	},
}

// Company-name parts ported verbatim from upstream's data-generator.ts.
var (
	namePrefixes = []string{
		"Apex", "Nova", "Prime", "Vertex", "Atlas", "Quantum", "Summit",
		"Nexus", "Titan", "Pinnacle", "Zenith", "Vanguard", "Horizon",
		"Stellar", "Onyx", "Cobalt", "Vector", "Pulse", "Forge", "Spark",
	}
	nameCores = []string{
		"Tech", "Data", "Cloud", "Logic", "Sync", "Flow", "Core", "Net",
		"Soft", "Wave", "Link", "Mind", "Byte", "Grid", "Hub",
	}
	nameSuffixes = []string{
		"Corp", "Inc", "Solutions", "Systems", "Labs", "Group", "Industries",
		"Dynamics", "Partners", "Ventures", "Global", "Digital",
	}
)

// gaussianRandom is the Box-Muller transform — ported byte-for-byte from
// upstream's gaussianRandom() so cluster shapes line up.
func gaussianRandom(rng *rand.Rand, mean, stdDev float64) float64 {
	u1 := rng.Float64()
	u2 := rng.Float64()
	z0 := math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2.0*math.Pi*u2)
	return z0*stdDev + mean
}

// clusteredValue draws one Gaussian sample centered in [min, max] with
// stdDev = (max-min)/4, then clamps to [min*0.8, max*1.2] so an occasional
// tail still lands plausibly. Mirrors upstream's generateClusteredValue().
func clusteredValue(rng *rand.Rand, min, max float64) float64 {
	mean := (min + max) / 2
	stdDev := (max - min) / 4
	v := gaussianRandom(rng, mean, stdDev)
	lo := min * 0.8
	hi := max * 1.2
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// selectSegment picks a segment by cumulative weight, falling back to SMB
// like upstream's selectSegment().
func selectSegment(rng *rand.Rand) string {
	r := rng.Float64()
	cum := 0.0
	for _, s := range segments {
		cum += segmentWeights[s]
		if r < cum {
			return s
		}
	}
	return "SMB"
}

// generateCompanyName composes a unique prefix-core-suffix triple. After
// 100 collisions it falls back to prefix+core+random-number, same as upstream.
func generateCompanyName(rng *rand.Rand, used map[string]bool) string {
	for i := 0; i < 100; i++ {
		p := namePrefixes[rng.IntN(len(namePrefixes))]
		c := nameCores[rng.IntN(len(nameCores))]
		s := nameSuffixes[rng.IntN(len(nameSuffixes))]
		name := p + " " + c + " " + s
		if !used[name] {
			used[name] = true
			return name
		}
	}
	p := namePrefixes[rng.IntN(len(namePrefixes))]
	c := nameCores[rng.IntN(len(nameCores))]
	return fmt.Sprintf("%s %s %d", p, c, rng.IntN(1000))
}

// generateCustomers materialises `count` customers using the seeded RNG.
// One deliberate Go-side change: rng is PCG(42, 42) so the visual baseline
// under __snapshots__/ stays stable. Upstream uses Math.random() which is
// fine for an interactive demo but not for a committed snapshot.
func generateCustomers(count int) []customer {
	rng := rand.New(rand.NewPCG(42, 42))
	used := map[string]bool{}
	out := make([]customer, 0, count)
	for i := 0; i < count; i++ {
		seg := selectSegment(rng)
		center := clusterCenters[seg]
		out = append(out, customer{
			ID:              fmt.Sprintf("cust-%04d", i+1),
			Name:            generateCompanyName(rng, used),
			Segment:         seg,
			AnnualRevenue:   int(math.Round(clusteredValue(rng, center.AnnualRevenue.Min, center.AnnualRevenue.Max))),
			EmployeeCount:   int(math.Round(clusteredValue(rng, center.EmployeeCount.Min, center.EmployeeCount.Max))),
			AccountAge:      int(math.Round(clusteredValue(rng, center.AccountAge.Min, center.AccountAge.Max))),
			EngagementScore: math.Round(clusteredValue(rng, center.EngagementScore.Min, center.EngagementScore.Max)),
			SupportTickets:  int(math.Round(clusteredValue(rng, center.SupportTickets.Min, center.SupportTickets.Max))),
			NPS:             int(math.Round(clusteredValue(rng, center.NPS.Min, center.NPS.Max))),
		})
	}
	return out
}

// generateSegmentSummaries collapses a customer slice into the four
// segment-summary rows the iframe legend renders.
func generateSegmentSummaries(custs []customer) []segmentSummary {
	counts := map[string]int{}
	for _, c := range custs {
		counts[c.Segment]++
	}
	out := make([]segmentSummary, 0, len(segments))
	for _, s := range segments {
		out = append(out, segmentSummary{Name: s, Count: counts[s], Color: segmentColors[s]})
	}
	return out
}

// Session-level cache — upstream's server.ts memoizes the full 250-customer
// generation across calls so the scatter plot stays stable as the iframe
// flips the segment dropdown. We do the same with sync.Once.
var (
	custOnce         sync.Once
	cachedCustomers  []customer
	cachedSummaries  []segmentSummary
)

func loadCustomers() ([]customer, []segmentSummary) {
	custOnce.Do(func() {
		cachedCustomers = generateCustomers(250)
		cachedSummaries = generateSegmentSummaries(cachedCustomers)
	})
	return cachedCustomers, cachedSummaries
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
					all, summaries := loadCustomers()
					filtered := all
					if in.Segment != "" && in.Segment != "All" {
						filtered = filtered[:0:0]
						for _, c := range all {
							if c.Segment == in.Segment {
								filtered = append(filtered, c)
							}
						}
					}
					return customerDataOutput{Customers: filtered, Segments: summaries}, nil
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

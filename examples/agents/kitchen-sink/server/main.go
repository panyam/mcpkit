// Command server is the kitchen-sink demo MCP server: a handful of typed
// tools chosen so every agent feature the harness turns on has something to
// exercise —
//
//	greet    a trivial tool, the "it works" check
//	report   returns a deliberately large document, so tool-result
//	         offloading (--offload-threshold) actually fires
//	analyze  summary stats over a list of numbers, a real task for a
//	         sub-agent persona to be delegated
//
// Streamable HTTP on :8788 (playground owns :8787, so both can run at once).
package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

type greetInput struct {
	Name string `json:"name" jsonschema:"description=Who to greet,required"`
}

type reportInput struct {
	Topic string `json:"topic" jsonschema:"description=What the report is about,required"`
}

type analyzeInput struct {
	Numbers []float64 `json:"numbers" jsonschema:"description=The numbers to summarize,required"`
}

func main() {
	srv := server.NewServer(
		core.ServerInfo{Name: "kitchen-sink-tools", Version: "0.1.0"},
		server.WithToolTimeout(30*time.Second),
	)

	srv.Register(core.TextTool[greetInput]("greet", "Say hello to someone by name",
		func(ctx core.ToolContext, in greetInput) (string, error) {
			return "Hello, " + in.Name + "!", nil
		},
	))

	srv.Register(core.TextTool[reportInput]("report",
		"Produce a long, detailed report on a topic (large output — good for exercising offloading)",
		func(ctx core.ToolContext, in reportInput) (string, error) {
			return buildReport(in.Topic), nil
		},
	))

	srv.Register(core.TextTool[analyzeInput]("analyze",
		"Compute summary statistics (count, sum, mean, min, max, stdev) for a list of numbers",
		func(ctx core.ToolContext, in analyzeInput) (string, error) {
			if len(in.Numbers) == 0 {
				return "", fmt.Errorf("analyze: need at least one number")
			}
			return analyze(in.Numbers), nil
		},
	))

	log.Println("kitchen-sink demo server listening on http://localhost:8788/mcp")
	if err := srv.Run(":8788"); err != nil {
		log.Fatal(err)
	}
}

// buildReport emits a multi-kilobyte document so a modest --offload-threshold
// trips: the result is stored and the model gets a stub + read_tool_result.
func buildReport(topic string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Report: %s\n\n", topic)
	sections := []string{"Background", "Current state", "Analysis", "Risks", "Recommendations", "Appendix"}
	for i, s := range sections {
		fmt.Fprintf(&b, "## %d. %s\n\n", i+1, s)
		for p := 1; p <= 6; p++ {
			fmt.Fprintf(&b, "Paragraph %d of the %q section on %s. ", p, s, topic)
			b.WriteString("It restates the point at length so the payload is comfortably over any small offload threshold, ")
			b.WriteString("which is exactly what we want the demo to show: the raw text lands in the store, not the context window. ")
			b.WriteString("The model sees a short stub and pays for the full text only if it calls read_tool_result.\n\n")
		}
	}
	return b.String()
}

func analyze(xs []float64) string {
	var sum, min, max float64
	min, max = xs[0], xs[0]
	for _, x := range xs {
		sum += x
		min = math.Min(min, x)
		max = math.Max(max, x)
	}
	mean := sum / float64(len(xs))
	var varsum float64
	for _, x := range xs {
		varsum += (x - mean) * (x - mean)
	}
	stdev := math.Sqrt(varsum / float64(len(xs)))
	return fmt.Sprintf("count=%d sum=%.4g mean=%.4g min=%.4g max=%.4g stdev=%.4g",
		len(xs), sum, mean, min, max, stdev)
}

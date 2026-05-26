// Example: SEP-2575 stateless wire — server fixture + SEP-2567 handle
// pattern demonstrator.
//
// Two halves bundled into one server:
//
//   - SEP-2567 cart tools (create_cart / add_item / checkout) demonstrate
//     the explicit-handle pattern via server.HandleStore[Cart]. Stateless
//     tools thread a server-minted cart_id through arguments rather than
//     relying on session-scoped storage; add_item updates in place via
//     HandleStore.Put so the cart_id stays stable across rounds.
//
//   - SEP-2575 diagnostic tools (test_missing_capability,
//     test_streaming_elicitation, test_logging_tool,
//     test_trigger_tool_change, test_trigger_prompt_change) implement
//     the contract the upstream conformance suite drives. The
//     scenario at modelcontextprotocol/conformance
//     `src/scenarios/server/stateless.ts` looks up each tool by name
//     and exercises the spec invariants against the wire behavior.
//
// Wire mode defaults to ModeStateless (the conformance fixture wants a
// pure-stateless surface) but can be flipped to dual or legacy via flag
// for interactive testing.
//
// Run modes:
//
//	go run . --serve              # MCP server on :8080 (pure stateless wire)
//	go run . --serve --mode=dual  # serves legacy AND stateless on one URL
//
// The walkthrough.go scripted demo is deferred — see panyam/mcpkit#478
// for the dual-mode walkthrough sweep.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	fmt.Println("examples/stateless — see --serve to run the MCP server.")
	fmt.Println("Conformance: cd ../../conformance && make testconf-stateless")
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	modeFlag := flag.String("mode", "stateless",
		"wire mode: legacy | dual | stateless (default stateless for conformance)")
	flag.CommandLine.Parse(filterArgs(os.Args[1:], "--serve"))

	mode, ok := stateless.ParseMode(*modeFlag)
	if !ok {
		log.Fatalf("invalid --mode: %q (want legacy|dual|stateless)", *modeFlag)
	}

	opts := common.MCPServerOptions(*addr, "[stateless] ")
	srv := server.NewServer(
		core.ServerInfo{Name: "stateless-demo", Version: "0.1.0"},
		opts...,
	)

	registerCartTools(srv, newCartStore())
	registerDiagnosticTools(srv)
	registerSeedPrompt(srv)

	log.Printf("[stateless-demo] mode=%s listening on %s", mode, *addr)
	if err := srv.ListenAndServe(
		server.WithStreamableHTTP(true),
		server.WithStatelessMode(mode),
	); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// filterArgs drops dispatch-style flags (like --serve) before handing
// the remaining args to flag.Parse.
func filterArgs(args []string, drop ...string) []string {
	dropSet := make(map[string]struct{}, len(drop))
	for _, d := range drop {
		dropSet[d] = struct{}{}
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if _, hit := dropSet[a]; hit {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ----- SEP-2567 handle pattern: cart -----

// Cart is the example stateful object — a shopping cart accumulated
// across multiple tool calls. Lives in HandleStore[Cart], keyed by
// the cart_id that create_cart returns and subsequent tools accept.
type Cart struct {
	Items []CartItem `json:"items"`
	Total float64    `json:"total"`
}

type CartItem struct {
	SKU      string  `json:"sku"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

// fixedPrices is a tiny in-memory catalog so add_item can compute a
// running total without an upstream service. Real implementations
// would look these up.
var fixedPrices = map[string]float64{
	"apple":  0.50,
	"bread":  2.50,
	"coffee": 4.00,
	"orange": 0.75,
}

// newCartStore returns the in-memory HandleStore[Cart]. Carts expire
// after 1 hour; the GC sweeps every 5 minutes.
func newCartStore() server.HandleStore[Cart] {
	return server.NewHandleStore[Cart](
		server.WithHandleIDPrefix("cart"),
		server.WithHandleDefaultTTL(time.Hour),
		server.WithHandleGCInterval(5*time.Minute),
	)
}

func registerCartTools(srv *server.Server, carts server.HandleStore[Cart]) {
	srv.RegisterTool(
		core.ToolDef{
			Name: "create_cart",
			Description: "Create an empty shopping cart and return its handle id. " +
				"SEP-2567: subsequent tools thread cart_id as a parameter.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			id := carts.Mint(Cart{}, 0)
			out := map[string]any{"cart_id": id}
			raw, _ := json.Marshal(out)
			return core.ToolResult{
				Content:           []core.Content{{Type: "text", Text: string(raw)}},
				StructuredContent: out,
			}, nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "add_item",
			Description: "Add an item to a cart. Updates the cart's running total in place.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cart_id":  map[string]any{"type": "string"},
					"sku":      map[string]any{"type": "string"},
					"quantity": map[string]any{"type": "integer", "minimum": 1},
				},
				"required": []string{"cart_id", "sku", "quantity"},
			},
		},
		func(_ core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				CartID   string `json:"cart_id"`
				SKU      string `json:"sku"`
				Quantity int    `json:"quantity"`
			}
			if err := json.Unmarshal(req.Arguments, &args); err != nil {
				return core.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			cart, ok := carts.Get(args.CartID)
			if !ok {
				return core.ToolResult{}, fmt.Errorf("unknown cart_id: %s", args.CartID)
			}
			price, known := fixedPrices[args.SKU]
			if !known {
				return core.ToolResult{}, fmt.Errorf(
					"unknown sku: %s (catalog: apple, bread, coffee, orange)", args.SKU)
			}
			cart.Items = append(cart.Items, CartItem{
				SKU:      args.SKU,
				Quantity: args.Quantity,
				Price:    price,
			})
			cart.Total += price * float64(args.Quantity)
			// Update-in-place under the same cart_id so the client's
			// handle stays valid across rounds (the SEP-2567 happy path).
			carts.Put(args.CartID, cart, 0)
			out := map[string]any{
				"cart_id": args.CartID,
				"total":   cart.Total,
				"items":   len(cart.Items),
			}
			raw, _ := json.Marshal(out)
			return core.ToolResult{
				Content:           []core.Content{{Type: "text", Text: string(raw)}},
				StructuredContent: out,
			}, nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "checkout",
			Description: "Check out a cart and return an order id. Deletes the cart handle.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"cart_id": map[string]any{"type": "string"}},
				"required":   []string{"cart_id"},
			},
		},
		func(_ core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				CartID string `json:"cart_id"`
			}
			if err := json.Unmarshal(req.Arguments, &args); err != nil {
				return core.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			cart, ok := carts.Get(args.CartID)
			if !ok {
				return core.ToolResult{}, fmt.Errorf("unknown cart_id: %s", args.CartID)
			}
			carts.Delete(args.CartID)
			orderID := fmt.Sprintf("order-%d", time.Now().UnixNano())
			out := map[string]any{
				"order_id": orderID,
				"total":    cart.Total,
				"items":    len(cart.Items),
			}
			raw, _ := json.Marshal(out)
			return core.ToolResult{
				Content:           []core.Content{{Type: "text", Text: string(raw)}},
				StructuredContent: out,
			}, nil
		},
	)
}

// ----- SEP-2575 diagnostic tools (conformance fixture) -----

func registerDiagnosticTools(srv *server.Server) {
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_missing_capability",
			Description: "SEP-2575 conformance hook: rejects with -32003 when the per-request _meta.clientCapabilities lacks 'sampling'.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			meta := stateless.RequestMetaFromContext(ctx)
			if meta == nil || meta.ClientCapabilities == nil || meta.ClientCapabilities.Sampling == nil {
				return core.ToolResult{}, &core.MissingCapabilityError{
					Required: core.ClientCapabilities{Sampling: &struct{}{}},
					Message:  "test_missing_capability requires the sampling capability",
				}
			}
			return core.TextResult("ok"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_streaming_elicitation",
			Description: "SEP-2575 conformance hook: emits an InputRequiredResult (MRTR) instead of a server-initiated request on the response stream.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"name": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message: "What is your name?",
						RequestedSchema: json.RawMessage(`{
							"type":"object",
							"properties":{"name":{"type":"string"}},
							"required":["name"]
						}`),
					}),
				})
			}
			return core.TextResult("hello"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_logging_tool",
			Description: "SEP-2575 conformance hook: emits notifications/message ONLY if the per-request _meta.logLevel is set.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			meta := stateless.RequestMetaFromContext(ctx)
			if meta != nil && meta.LogLevel != "" {
				if lvl, ok := core.ParseLogLevel(meta.LogLevel); ok {
					core.EmitLog(ctx, lvl, "test_logging_tool",
						"log line emitted because _meta.logLevel was set")
				}
			}
			return core.TextResult("done"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_trigger_tool_change",
			Description: "SEP-2575 conformance hook: mutates the tool registry to broadcast notifications/tools/list_changed.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			name := fmt.Sprintf("_synthetic_%d", time.Now().UnixNano())
			_ = srv.Registry().AddTool(
				core.ToolDef{Name: name, Description: "synthetic"},
				func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
					return core.TextResult("synthetic"), nil
				},
			)
			srv.Registry().RemoveTool(name)
			return core.TextResult("toolsListChanged broadcast"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_trigger_prompt_change",
			Description: "SEP-2575 conformance hook: mutates the prompt registry to broadcast notifications/prompts/list_changed.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			name := fmt.Sprintf("_synthetic_prompt_%d", time.Now().UnixNano())
			_ = srv.Registry().AddPrompt(
				core.PromptDef{Name: name, Description: "synthetic"},
				func(_ core.PromptContext, _ core.PromptRequest) (core.PromptResult, error) {
					return core.PromptResult{}, nil
				},
			)
			srv.Registry().RemovePrompt(name)
			return core.TextResult("promptsListChanged broadcast"), nil
		},
	)
}

// registerSeedPrompt registers one stable prompt so prompts/list returns
// something non-empty pre-mutation. Lets the conformance scenario
// verify that prompts capability is correctly advertised in discover.
func registerSeedPrompt(srv *server.Server) {
	if err := srv.Registry().AddPrompt(
		core.PromptDef{
			Name:        "greeting",
			Description: "Demo prompt so prompts/list and prompts cap are non-empty.",
		},
		func(_ core.PromptContext, _ core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Hello, world."},
				}},
			}, nil
		},
	); err != nil {
		log.Fatalf("seed prompt: %v", err)
	}
}

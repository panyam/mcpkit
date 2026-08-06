package server_test

import (
	"context"
	"encoding/json"
	"fmt"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func exampleDispatch(srv *server.Server, method string, params any) *core.Response {
	raw, _ := json.Marshal(params)
	resp, _ := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  core.NewRawJSON(raw),
	})
	return resp
}

// Subscribing and unsubscribing from a resource. Subscription requires the
// WithSubscriptions server option; without it the methods are not advertised.
// Unsubscribe is what stops notifications/resources/updated for that URI, and a
// session that disconnects is unsubscribed automatically.
func ExampleWithSubscriptions() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"}, server.WithSubscriptions())
	srv.RegisterResource(
		core.ResourceDef{URI: "feed://prices", Name: "prices"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: "feed://prices", Text: "42",
			}}}, nil
		},
	)
	testutil.InitHandshake(srv)

	sub := exampleDispatch(srv, "resources/subscribe", map[string]string{"uri": "feed://prices"})
	unsub := exampleDispatch(srv, "resources/unsubscribe", map[string]string{"uri": "feed://prices"})

	fmt.Println(sub.Error == nil, unsub.Error == nil)
	// Output: true true
}

// Setting the log level. The client raises or lowers the threshold at runtime
// and the server filters notifications/message below it, so a client can turn
// on debug output without a reconnect.
func ExampleServer_Dispatch_loggingSetLevel() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})
	testutil.InitHandshake(srv)

	ok := exampleDispatch(srv, "logging/setLevel", map[string]string{"level": "debug"})
	bad := exampleDispatch(srv, "logging/setLevel", map[string]string{"level": "louder"})

	fmt.Println(ok.Error == nil, bad.Error.Message)
	// Output: true unknown log level: louder
}

// Pagination on a list method. mcpkit servers return every item in one page:
// the page size is a compile-time constant of 0, meaning "no pagination", so
// nextCursor is always empty regardless of how many items are registered.
//
// The cursor is still part of the wire contract, and mcpkit's client handles
// servers that do paginate. Loop with ListToolsPage, passing the previous
// NextCursor, until it comes back empty.
func ExampleServer_Dispatch_pagination() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})
	for i := range 3 {
		name := fmt.Sprintf("tool-%d", i)
		srv.Register(core.TextTool[struct{}](name, "noop",
			func(ctx core.ToolContext, _ struct{}) (string, error) { return "", nil }))
	}
	testutil.InitHandshake(srv)

	resp := exampleDispatch(srv, "tools/list", map[string]any{})
	var out core.ToolsListResult
	_ = resp.ResultAs(&out)

	fmt.Println(len(out.Tools), out.NextCursor == "")
	// Output: 3 true
}

// An elicitation schema with an enum. The client renders a fixed choice rather
// than a free-text field. enumNames is optional and supplies display labels
// parallel to enum; a client that does not understand it falls back to the raw
// enum values.
//
// mcpkit does not validate the response against this schema. The schema drives
// what the client collects; a handler that cares should check the returned
// content itself.
func ExampleElicitationRequest_enum() {
	req := core.ElicitationRequest{
		Message: "Pick a deployment target",
		RequestedSchema: json.RawMessage(`{
                        "type": "object",
                        "properties": {
                          "env": {
                            "type": "string",
                            "enum": ["dev", "staging", "prod"],
                            "enumNames": ["Development", "Staging", "Production"],
                            "default": "dev"
                          }
                        },
                        "required": ["env"]
                        }`),
	}

	var schema struct {
		Properties struct {
			Env struct {
				Enum    []string `json:"enum"`
				Default string   `json:"default"`
			} `json:"env"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(req.RequestedSchema, &schema)

	fmt.Println(schema.Properties.Env.Enum, schema.Properties.Env.Default)
	// Output: [dev staging prod] dev
}

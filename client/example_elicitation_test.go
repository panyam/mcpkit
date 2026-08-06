package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/client"
)

func elicitRequest(schema string) *core.Request {
	params, _ := json.Marshal(core.ElicitationRequest{
		Message:         "Configure the deploy",
		RequestedSchema: json.RawMessage(schema),
	})
	return &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "elicitation/create",
		Params:  core.NewRawJSON(params),
	}
}

func elicitContent(resp *core.Response) string {
	var out core.ElicitationResult
	_ = resp.ResultAs(&out)
	keys := make([]string, 0, len(out.Content))
	for k := range out.Content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, out.Content[k]))
	}
	return fmt.Sprint(parts)
}

const deploySchema = `{
  "type": "object",
  "properties": {
    "env":     {"type": "string",  "default": "dev"},
    "replicas":{"type": "integer", "default": 2},
    "verbose": {"type": "boolean", "default": false}
  }
}`

// SEP-1034 defaults. A schema property may declare a default; the client fills
// it in for any key the user left out, so a handler can return exactly what the
// user typed and stay unaware of defaults entirely.
//
// The merge happens inside the elicitation/create dispatch path, after your
// handler returns and before the response goes back to the server.
func ExampleWithElicitationHandler_defaults() {
	c := client.NewClient("http://example.invalid",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithElicitationHandler(
			func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
				// The user filled in one field and left the rest alone.
				return core.ElicitationResult{
					Action:  "accept",
					Content: map[string]any{"env": "prod"},
				}, nil
			},
		),
	)

	resp := c.HandleServerRequestWithContext(context.Background(), elicitRequest(deploySchema))

	fmt.Println(elicitContent(resp))
	// Output: [env=prod replicas=2 verbose=false]
}

// A value the user supplied always wins. Defaults only fill absent keys, so
// this never overwrites input, including a value that happens to equal the
// zero value of its type.
func ExampleWithElicitationHandler_defaultsDoNotOverwrite() {
	c := client.NewClient("http://example.invalid",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithElicitationHandler(
			func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
				return core.ElicitationResult{
					Action:  "accept",
					Content: map[string]any{"env": "staging", "replicas": 0},
				}, nil
			},
		),
	)

	resp := c.HandleServerRequestWithContext(context.Background(), elicitRequest(deploySchema))

	fmt.Println(elicitContent(resp))
	// Output: [env=staging replicas=0 verbose=false]
}

// Defaults apply only when the user accepted. On decline or cancel the result's
// content is undefined, so filling defaults would invent input the user never
// gave.
func ExampleWithElicitationHandler_defaultsSkippedOnDecline() {
	c := client.NewClient("http://example.invalid",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithElicitationHandler(
			func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
				return core.ElicitationResult{Action: "decline"}, nil
			},
		),
	)

	resp := c.HandleServerRequestWithContext(context.Background(), elicitRequest(deploySchema))

	var out core.ElicitationResult
	_ = resp.ResultAs(&out)
	fmt.Println(out.Action, len(out.Content))
	// Output: decline 0
}

// A default whose type contradicts the schema is dropped rather than sent.
// Forwarding it would put wire-invalid data in front of the server, so the key
// is simply left absent and the server sees an omitted field.
func ExampleWithElicitationHandler_defaultsTypeMismatch() {
	const badSchema = `{
      "type": "object",
      "properties": {
        "replicas": {"type": "integer", "default": "two"},
        "env":      {"type": "string",  "default": "dev"}
      }
    }`

	c := client.NewClient("http://example.invalid",
		core.ClientInfo{Name: "example", Version: "1.0"},
		client.WithElicitationHandler(
			func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
				return core.ElicitationResult{Action: "accept", Content: map[string]any{}}, nil
			},
		),
	)

	resp := c.HandleServerRequestWithContext(context.Background(), elicitRequest(badSchema))

	fmt.Println(elicitContent(resp))
	// Output: [env=dev]
}

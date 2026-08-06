package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func completionRequest(refType, name, argName, partial string) *core.Request {
	params, _ := json.Marshal(map[string]any{
		"ref":      map[string]string{"type": refType, "name": name},
		"argument": map[string]string{"name": argName, "value": partial},
	})
	return &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  core.NewRawJSON(params),
	}
}

func completionValues(resp *core.Response) core.CompletionResult {
	var out struct {
		Completion core.CompletionResult `json:"completion"`
	}
	_ = resp.ResultAs(&out)
	return out.Completion
}

// Completion suggestions for a prompt argument. The client sends the partial
// value the user has typed; the handler returns the matching candidates.
func ExampleServer_RegisterCompletion() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterCompletion("ref/prompt", "summarize",
		func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
			styles := []string{"bullet", "brief", "detailed"}
			var matched []string
			for _, s := range styles {
				if strings.HasPrefix(s, arg.Value) {
					matched = append(matched, s)
				}
			}
			return core.CompletionResult{Values: matched, Total: len(matched)}, nil
		},
	)
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		completionRequest("ref/prompt", "summarize", "style", "b"))

	fmt.Println(completionValues(resp).Values)
	// Output: [bullet brief]
}

// Completion for a resource URI template argument. The ref type is
// "ref/resource" and the registered name is the URI template itself, not a
// prompt name.
func ExampleServer_RegisterCompletion_resource() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterCompletion("ref/resource", "file:///logs/{date}",
		func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
			return core.CompletionResult{
				Values: []string{"2026-08-04", "2026-08-05"},
				Total:  2,
			}, nil
		},
	)
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		completionRequest("ref/resource", "file:///logs/{date}", "date", "2026-08"))

	fmt.Println(completionValues(resp).Values)
	// Output: [2026-08-04 2026-08-05]
}

// An unregistered ref is not an error. The server answers with an empty list so
// a client can offer completion everywhere without first checking which
// arguments support it.
func ExampleServer_RegisterCompletion_unregistered() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		completionRequest("ref/prompt", "no-such-prompt", "style", "b"))

	result := completionValues(resp)
	fmt.Println(resp.Error == nil, len(result.Values))
	// Output: true 0
}

// The spec caps a completion response at 100 values. Returning more is allowed:
// the server truncates, sets Total to the full count, and flips HasMore, so a
// handler can stay simple and return everything it found.
func ExampleServer_RegisterCompletion_truncation() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterCompletion("ref/prompt", "pick",
		func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
			values := make([]string, 150)
			for i := range values {
				values[i] = fmt.Sprintf("item-%d", i)
			}
			return core.CompletionResult{Values: values}, nil
		},
	)
	testutil.InitHandshake(srv)

	resp, _ := srv.Dispatch(context.Background(),
		completionRequest("ref/prompt", "pick", "item", ""))

	result := completionValues(resp)
	fmt.Println(len(result.Values), result.Total, result.HasMore)
	// Output: 100 150 true
}

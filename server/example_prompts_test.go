package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func exampleGetPrompt(srv *server.Server, name string, args map[string]any) core.PromptResult {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	resp, _ := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "prompts/get",
		Params:  core.NewRawJSON(params),
	})
	var out core.PromptResult
	_ = resp.ResultAs(&out)
	return out
}

// A prompt with no arguments. The handler returns messages the client will feed
// to a model, so Role is "user" or "assistant" and the content is what you want
// in the conversation.
func ExampleServer_RegisterPrompt() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{Name: "changelog", Description: "Draft a changelog entry"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{
				Description: "Changelog draft",
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Draft a changelog entry."},
				}},
			}, nil
		},
	)
	testutil.InitHandshake(srv)

	res := exampleGetPrompt(srv, "changelog", nil)
	fmt.Println(res.Messages[0].Role, res.Messages[0].Content.Text)
	// Output: user Draft a changelog entry.
}

// A prompt with arguments. Declare them on PromptDef so a client can present
// them, then read them from req.Arguments.
//
// Arguments arrive as decoded JSON values in a map[string]any, so a string
// argument needs a type assertion. Give an argument a Schema and the dispatcher
// validates it before your handler runs, rejecting bad input with -32602.
func ExampleServer_RegisterPrompt_arguments() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{
			Name: "review",
			Arguments: []core.PromptArgument{
				{Name: "lang", Description: "Source language", Required: true,
					Schema: map[string]any{"type": "string"}},
			},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			lang, _ := req.Arguments["lang"].(string)
			return core.PromptResult{Messages: []core.PromptMessage{{
				Role:    "user",
				Content: core.Content{Type: "text", Text: "Review this " + lang + " code."},
			}}}, nil
		},
	)
	testutil.InitHandshake(srv)

	res := exampleGetPrompt(srv, "review", map[string]any{"lang": "Go"})
	fmt.Println(res.Messages[0].Content.Text)
	// Output: Review this Go code.
}

// A prompt carrying an image. PromptMessage.Content is the same Content type
// tool results use, so Type "image" plus base64 Data and a MimeType works
// identically here.
func ExampleServer_RegisterPrompt_image() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{Name: "describe", Description: "Describe a screenshot"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{Messages: []core.PromptMessage{{
				Role: "user",
				Content: core.Content{
					Type:     "image",
					Data:     base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}),
					MimeType: "image/png",
				},
			}}}, nil
		},
	)
	testutil.InitHandshake(srv)

	c := exampleGetPrompt(srv, "describe", nil).Messages[0].Content
	fmt.Println(c.Type, c.MimeType, c.Data)
	// Output: image image/png iVBORw==
}

// A prompt embedding a resource. Use this instead of inlining text when the
// content has a URI the client already knows, so it can attribute or re-fetch
// it rather than treating it as anonymous prose.
func ExampleServer_RegisterPrompt_embeddedResource() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{Name: "explain", Description: "Explain a config file"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{Messages: []core.PromptMessage{{
				Role: "user",
				Content: core.Content{
					Type: "resource",
					Resource: &core.ResourceContent{
						URI:      "file:///etc/app.conf",
						MimeType: "text/plain",
						Text:     "debug = true",
					},
				},
			}}}, nil
		},
	)
	testutil.InitHandshake(srv)

	c := exampleGetPrompt(srv, "explain", nil).Messages[0].Content
	fmt.Println(c.Type, c.Resource.URI, c.Resource.Text)
	// Output: resource file:///etc/app.conf debug = true
}

// Listing prompts. Clients call this to populate a picker, so Description and
// Arguments are what a user sees before choosing.
func ExampleServer_Dispatch_promptsList() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "review",
			Description: "Review a diff",
			Arguments:   []core.PromptArgument{{Name: "lang", Required: true}},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		},
	)
	testutil.InitHandshake(srv)

	resp := exampleDispatch(srv, "prompts/list", map[string]any{})
	var out core.PromptsListResult
	_ = resp.ResultAs(&out)

	p := out.Prompts[0]
	fmt.Println(p.Name, p.Description, p.Arguments[0].Name, p.Arguments[0].Required)
	// Output: review Review a diff lang true
}

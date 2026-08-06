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

func exampleCallTool(srv *server.Server, name string) core.ToolResult {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
	resp, _ := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  core.NewRawJSON(params),
	})
	var out core.ToolResult
	_ = resp.ResultAs(&out)
	return out
}

func exampleReadResource(srv *server.Server, uri string) core.ResourceResult {
	params, _ := json.Marshal(map[string]string{"uri": uri})
	resp, _ := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "resources/read",
		Params:  core.NewRawJSON(params),
	})
	var out core.ResourceResult
	_ = resp.ResultAs(&out)
	return out
}

// A tool returning an image. Data is base64, and MimeType tells the client how
// to decode it. The same Content shape carries audio and embedded resources,
// switched by Type.
func ExampleServer_Register_imageResult() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	png := []byte{0x89, 'P', 'N', 'G'}
	srv.Register(core.TypedTool[struct{}, core.ToolResult]("render", "Renders a chart",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			return core.ToolResult{Content: []core.Content{{
				Type:     "image",
				Data:     base64.StdEncoding.EncodeToString(png),
				MimeType: "image/png",
			}}}, nil
		},
	))
	testutil.InitHandshake(srv)

	c := exampleCallTool(srv, "render").Content[0]
	fmt.Println(c.Type, c.MimeType, c.Data)
	// Output: image image/png iVBORw==
}

// A tool returning audio. Identical to the image case apart from Type, which is
// why neither needs a dedicated result helper.
func ExampleServer_Register_audioResult() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.Register(core.TypedTool[struct{}, core.ToolResult]("speak", "Synthesizes speech",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			return core.ToolResult{Content: []core.Content{{
				Type:     "audio",
				Data:     base64.StdEncoding.EncodeToString([]byte("RIFF")),
				MimeType: "audio/wav",
			}}}, nil
		},
	))
	testutil.InitHandshake(srv)

	c := exampleCallTool(srv, "speak").Content[0]
	fmt.Println(c.Type, c.MimeType)
	// Output: audio audio/wav
}

// A tool embedding a resource in its result. Use this when the tool produced
// something the client should be able to refer to later by URI, rather than an
// opaque blob. Text and Blob are mutually exclusive on the embedded content.
func ExampleServer_Register_embeddedResource() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.Register(core.TypedTool[struct{}, core.ToolResult]("report", "Generates a report",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			return core.ToolResult{Content: []core.Content{{
				Type: "resource",
				Resource: &core.ResourceContent{
					URI:      "report://q3",
					MimeType: "text/plain",
					Text:     "revenue up 4%",
				},
			}}}, nil
		},
	))
	testutil.InitHandshake(srv)

	c := exampleCallTool(srv, "report").Content[0]
	fmt.Println(c.Type, c.Resource.URI, c.Resource.Text)
	// Output: resource report://q3 revenue up 4%
}

// Reading a binary resource. Set Blob (base64) instead of Text; the two are
// mutually exclusive, and MimeType is what tells the client which it is getting.
func ExampleServer_RegisterResource_binary() {
	srv := server.NewServer(core.ServerInfo{Name: "docs", Version: "1.0"})

	srv.RegisterResource(
		core.ResourceDef{URI: "asset://logo.png", Name: "logo", MimeType: "image/png"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      "asset://logo.png",
				MimeType: "image/png",
				Blob:     base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}),
			}}}, nil
		},
	)
	testutil.InitHandshake(srv)

	c := exampleReadResource(srv, "asset://logo.png").Contents[0]
	fmt.Println(c.MimeType, c.Blob, c.Text == "")
	// Output: image/png iVBORw== true
}

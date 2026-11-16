// Package mcp holds the list-ttl server's tools, resources and prompts. It
// imports neither demokit nor otel, so the browser build in ../web stays
// small (#1714).
package mcp

import (
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Options returns the SEP-2549 cache hints the demo serves. A negative ttlMs
// and an empty scope leave both hints off the list and read responses.
func Options(ttlMs int, scope string) []server.Option {
	if ttlMs < 0 && scope == "" {
		return nil
	}
	return []server.Option{
		server.WithListCacheControl(ttlMs, scope),
		server.WithReadResourceCacheControl(ttlMs, scope),
	}
}

// Register adds one tool, one resource, one resource template and one prompt
// to srv, so each list method has something to carry the cache hints. The CLI
// server and the browser build (../web) both call it.
func Register(srv *server.Server) {
	srv.Register(core.TextTool[struct{}]("ping", "Returns 'pong'",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "pong", nil
		},
	))

	srv.RegisterResource(
		core.ResourceDef{
			URI:      "file:///fixture",
			Name:     "fixture",
			MimeType: "text/plain",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      req.URI,
					MimeType: "text/plain",
					Text:     "fixture",
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{URITemplate: "file:///t/{name}", Name: "tmpl"},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      uri,
					MimeType: "text/plain",
					Text:     "templated:" + params["name"],
				}},
			}, nil
		},
	)

	srv.RegisterPrompt(
		core.PromptDef{Name: "hello", Description: "Sample prompt"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		},
	)
}

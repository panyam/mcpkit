package main

// Conformance resources implement the resource contracts expected by the official
// MCP conformance test suite (@modelcontextprotocol/conformance).

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// registerConformanceResources adds all resources required by the MCP conformance suite.
func registerConformanceResources(srv *server.Server) {
	// test://static-text — returns plain text content
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "test://static-text",
			Name:        "Static Text Resource",
			Description: "A static text resource for conformance testing",
			MimeType:    "text/plain",
		},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      "test://static-text",
					MimeType: "text/plain",
					Text:     "This is a test resource",
				}},
			}, nil
		},
	)

	// test://static-binary — returns base64 binary content
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "test://static-binary",
			Name:        "Static Binary Resource",
			Description: "A static binary resource for conformance testing",
			MimeType:    "application/octet-stream",
		},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			data := []byte("binary test data")
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      "test://static-binary",
					MimeType: "application/octet-stream",
					Blob:     base64.StdEncoding.EncodeToString(data),
				}},
			}, nil
		},
	)

	// test://template/{id}/data — URI template resource
	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "test://template/{id}/data",
			Name:        "Template Resource",
			Description: "A parameterized resource template for conformance testing",
			MimeType:    "text/plain",
		},
		func(ctx context.Context, uri string, params map[string]string) (core.ResourceResult, error) {
			id := params["id"]
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      uri,
					MimeType: "text/plain",
					Text:     fmt.Sprintf("Template data for ID: %s", id),
				}},
			}, nil
		},
	)
}

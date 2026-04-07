// Package ui provides the MCP Apps extension (io.modelcontextprotocol/ui)
// for mcpkit servers. It declares the extension in the initialize response
// so clients know the server supports interactive HTML UIs.
//
// This is a separate Go module (github.com/panyam/mcpkit/ext/ui) so that
// the core mcpkit module stays zero-deps. Import this package to advertise
// MCP Apps support on your server.
//
// Usage:
//
//	srv := server.NewServer(info,
//	    server.WithExtension(ui.UIExtension{}),
//	)
package ui

import "github.com/panyam/mcpkit/core"

// UIExtension declares support for the MCP Apps extension.
// Register it on the server to advertise UI rendering capability
// in the initialize response.
type UIExtension struct{}

// Extension returns the MCP Apps extension metadata.
func (UIExtension) Extension() core.Extension {
	return core.Extension{
		ID:          core.UIExtensionID,
		SpecVersion: "2026-01-26",
		Stability:   core.Experimental,
	}
}

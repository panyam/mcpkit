// Package auth provides MCP authorization support backed by oneauth.
// It implements mcpkit's AuthValidator, ClaimsProvider, TokenSource, and
// ExtensionProvider interfaces as thin adapters over oneauth's JWT, OAuth,
// and discovery primitives.
//
// This is a separate Go module (github.com/panyam/mcpkit/auth) so that
// the core mcpkit module stays zero-auth-deps. Import this package only
// when you need JWT validation, OAuth flows, or PRM endpoints.
package auth

import "github.com/panyam/mcpkit/core"

// AuthExtension declares the MCP auth extension with its spec version
// and stability level. Register it on the server to advertise auth
// support in the initialize response.
//
// Usage:
//
//	srv := core.NewServer(info,
//	    core.WithAuth(jwtValidator),
//	    core.WithExtension(auth.AuthExtension{}),
//	)
type AuthExtension struct{}

// Extension returns the MCP auth extension metadata.
func (AuthExtension) Extension() core.Extension {
	return core.Extension{
		ID:          "io.mcpkit/auth",
		SpecVersion: "2025-11-25",
		Stability:   core.Experimental,
	}
}

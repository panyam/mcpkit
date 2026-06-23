// Example: SEP-2350 step-up auth against Keycloak.
//
// Experimental. SEP-2350 (client-side scope accumulation in step-up
// authorization) is merged into the spec, but the server-side reference
// implementation in modelcontextprotocol/typescript-sdk PR 1624 is still under
// review. This example exists to demonstrate that mcpkit's
// ext/auth.NewToolScopeMiddleware emits the same wire shape PR 1624's
// reference impl emits, and to drive the conformance scenario in
// panyam/mcpconformance PR 19 (the fork) against a real Keycloak realm.
// Both will move once upstream lands; expect API or naming churn here.
//
// What this does:
//
//   - Wires ext/auth.JWTValidator against the Keycloak realm at KEYCLOAK_URL
//     (default http://localhost:8180/realms/mcpkit-test).
//   - Registers one scope-gated tool, admin_call, requiring the admin-write
//     scope (matching the scope-challenge scenario's default context).
//   - Installs auth.NewToolScopeMiddleware so a tools/call with an
//     under-scoped token returns HTTP 403 with a WWW-Authenticate Bearer
//     insufficient_scope challenge per RFC 6750 Section 3.1, instead of
//     reaching the handler.
//   - Publishes the Protected Resource Metadata document at
//     /.well-known/oauth-protected-resource/mcp pointing at the Keycloak
//     realm as the authorization server.
//
// Operator runbook lives in README.md alongside this file. The canonical
// end-to-end runbook (including how to drive the conformance scenario
// against this server) is panyam/mcpconformance examples/auth-fixtures/
// keycloak/README.md on the feat/sep-2350-server-scope-challenge branch.
//
// Run:
//
//	go run ./examples/auth/step-up-keycloak \
//	    -addr :3020 \
//	    -keycloak-url http://localhost:8180 \
//	    -realm mcpkit-test
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/panyam/mcpkit/core"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	oneauthclient "github.com/panyam/oneauth/client"
)

const (
	defaultKeycloakURL = "http://localhost:8180"
	defaultRealm       = "mcpkit-test"
	scopeToolsRead     = "tools-read"
	scopeToolsCall     = "tools-call"
	scopeAdminWrite    = "admin-write"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", ":3020", "listen address")
	kcURL := flag.String("keycloak-url", envOr("KEYCLOAK_URL", defaultKeycloakURL), "Keycloak base URL")
	realm := flag.String("realm", envOr("KC_REALM", defaultRealm), "Keycloak realm name")
	flag.Parse()

	realmURL := fmt.Sprintf("%s/realms/%s", *kcURL, *realm)

	// AS discovery via oneauth — same RFC 8414 + OIDC fallback path mcpkit's
	// ext/auth.DiscoverMCPAuth uses internally. Avoids hand-rolling a
	// well-known fetch + JSON decode, and keeps the example aligned with
	// "oneauth is the strategic destination for MCP auth" — for any
	// production deployment, this is the same helper you'd reach for.
	asMeta, err := oneauthclient.DiscoverAS(realmURL)
	if err != nil {
		log.Fatalf("AS discovery failed against %s: %v", realmURL, err)
	}
	issuer := asMeta.Issuer
	jwksURI := asMeta.JWKSURI

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	allScopes := []string{scopeToolsRead, scopeToolsCall, scopeAdminWrite}

	prmURL := listenURL + "/.well-known/oauth-protected-resource/mcp"
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             jwksURI,
		Issuer:              issuer,
		Audience:            "", // Keycloak doesn't set aud on client_credentials by default.
		ResourceMetadataURL: prmURL,
		AllScopes:           allScopes,
	})
	validator.Start()
	defer validator.Stop()

	log.Printf("step-up-keycloak: serving %s/mcp", listenURL)
	log.Printf("  AS issuer:  %s", issuer)
	log.Printf("  JWKS URI:   %s", jwksURI)
	log.Printf("  scope-gated tool: admin_call requires '%s'", scopeAdminWrite)

	cfg := mcpcommon.ServerConfig{
		Name:    "step-up-keycloak",
		Version: "0.1.0-experimental",
		Addr:    *addr,
		Options: []server.Option{
			server.WithAuth(validator),
		},
		Register: func(srv *server.Server) {
			srv.Register(core.TextTool[struct{}]("admin_call",
				"Requires admin-write scope. Returns ok when scope is sufficient; otherwise the scope middleware returns HTTP 403 with WWW-Authenticate before this handler runs.",
				func(_ core.ToolContext, _ struct{}) (string, error) {
					return "admin_call: ok", nil
				},
				core.WithToolRequiredScopes(scopeAdminWrite),
			))
			srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry(),
				auth.WithResourceMetadataURL(prmURL),
			))
		},
		TransportOptions: []server.TransportOption{
			// Dual mode (default) so the scenario's legacy initialize +
			// session-id handshake works. modelcontextprotocol/typescript-sdk
			// PR 1624's transport does not yet support the SEP-2575
			// stateless wire (protocolVersion 2026-07-28), so the
			// conformance scenario speaks the legacy 2025-11-25 wire to
			// stay interop-compatible across both SUTs.
			server.WithMux(func(mux *http.ServeMux) {
				auth.MountAuth(mux, auth.AuthConfig{
					ResourceURI:          listenURL,
					AuthorizationServers: []string{issuer},
					ScopesSupported:      allScopes,
					MCPPath:              "/mcp",
				})
			}),
		},
	}
	if err := mcpcommon.RunServer(cfg); err != nil {
		log.Fatal(err)
	}
}

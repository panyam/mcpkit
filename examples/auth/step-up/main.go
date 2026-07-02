// Example: SEP-2350 step-up auth against any OAuth2 authorization server.
//
// Experimental. Provider-neutral successor to step-up-keycloak: the SEP-2350
// scope-challenge wire shape is provider-blind (RFC 6750 Section 3.1 + RFC 9728
// PRM discovery), so a single SUT validates it against any RFC-compliant AS.
// Point -issuer at Keycloak, Okta, Entra, WorkOS, Descope, etc. Provider
// specifics (token provisioning, scope minting) live in the per-provider
// fixtures under panyam/mcpconformance examples/auth-fixtures/<provider>.
//
// What this does:
//
//   - Discovers the authorization server at -issuer via oneauth's RFC 8414 +
//     OIDC-fallback path.
//   - Validates access tokens regardless of which claim shape the IdP uses for
//     scopes (scope string, scopes array, scp array) — handled by
//     ext/auth.JWTValidator.
//   - Registers one scope-gated tool, admin_call, requiring admin-write.
//   - Installs auth.NewToolScopeMiddleware so an under-scoped tools/call
//     returns HTTP 403 + WWW-Authenticate Bearer insufficient_scope per
//     RFC 6750 Section 3.1, instead of reaching the handler.
//   - Publishes Protected Resource Metadata at
//     /.well-known/oauth-protected-resource/mcp pointing at the AS.
//
// Runbook: README.md alongside this file. The scope-challenge conformance
// scenario + per-provider fixtures live in panyam/mcpconformance.
//
// Run (Keycloak):
//
//	go run ./examples/auth/step-up \
//	    -issuer http://localhost:8180/realms/mcpkit-test
//
// Run (Okta — sets aud on client_credentials, so pass -audience):
//
//	source <mcpconformance>/examples/auth-fixtures/okta/okta.env
//	go run ./examples/auth/step-up -issuer "$OKTA_ISSUER" -audience api://default
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
	// defaultIssuer points at a local Keycloak realm — free, hermetic, no cloud
	// account. Override -issuer for Okta/Entra/etc. (see README).
	defaultIssuer   = "http://localhost:8180/realms/mcpkit-test"
	scopeToolsRead  = "tools-read"
	scopeToolsCall  = "tools-call"
	scopeAdminWrite = "admin-write"
	scopeAdmin      = "admin"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", ":3021", "listen address")
	issuerFlag := flag.String("issuer", envOr("MCP_ISSUER", defaultIssuer),
		"authorization server issuer URL; defaults to a local Keycloak realm. For Okta: https://<tenant>.okta.com/oauth2/default")
	audience := flag.String("audience", envOr("MCP_AUDIENCE", ""),
		"expected aud claim on access tokens (empty = skip; Okta needs api://default, Keycloak leaves it unset)")
	wire := mcpcommon.RegisterWireFlags(flag.CommandLine)
	flag.Parse()

	if *issuerFlag == "" {
		log.Fatal("issuer required: set -issuer or MCP_ISSUER")
	}

	// AS discovery via oneauth — RFC 8414 with path insertion + OIDC fallback,
	// which both Keycloak realms and Okta custom authorization servers serve.
	// Keeps the example aligned with "oneauth is the strategic destination for
	// MCP auth."
	asMeta, err := oneauthclient.DiscoverAS(*issuerFlag)
	if err != nil {
		log.Fatalf("AS discovery failed against %s: %v", *issuerFlag, err)
	}
	issuer := asMeta.Issuer
	jwksURI := asMeta.JWKSURI

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	allScopes := []string{scopeToolsRead, scopeToolsCall, scopeAdminWrite, scopeAdmin}

	prmURL := listenURL + "/.well-known/oauth-protected-resource/mcp"
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             jwksURI,
		Issuer:              issuer,
		Audience:            *audience,
		ResourceMetadataURL: prmURL,
		AllScopes:           allScopes,
	})
	validator.Start()
	defer validator.Stop()

	log.Printf("step-up: serving %s/mcp", listenURL)
	log.Printf("  AS issuer:  %s", issuer)
	log.Printf("  JWKS URI:   %s", jwksURI)
	if *audience != "" {
		log.Printf("  audience:   %s", *audience)
	} else {
		log.Printf("  audience:   (unset — aud not validated)")
	}
	log.Printf("  scope-gated tool: admin_call requires '%s'", scopeAdminWrite)

	cfg := mcpcommon.ServerConfig{
		Name:    "step-up",
		Version: "0.1.0-experimental",
		Addr:    *addr,
		Wire:    wire,
		Options: []server.Option{
			server.WithAuth(validator),
		},
		Register: func(srv *server.Server) {
			// admin_call demonstrates the SEP-2350 OR-hierarchy: a caller
			// satisfies the gate by holding ANY scope in AcceptedScopes, even
			// if the literal admin-write scope is absent. The 403 challenge
			// still advertises requiredScopes only (never accepted-parents)
			// per the least-privilege rule.
			srv.Register(core.TextTool[struct{}]("admin_call",
				"Requires admin-write scope. The OR-hierarchy on AcceptedScopes lets a token with the parent admin scope satisfy the gate too.",
				func(_ core.ToolContext, _ struct{}) (string, error) {
					return "admin_call: ok", nil
				},
				core.WithToolRequiredScopes(scopeAdminWrite),
				core.WithToolAcceptedScopes(scopeAdminWrite, scopeAdmin),
			))
			srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry(),
				auth.WithResourceMetadataURL(prmURL),
			))
		},
		TransportOptions: []server.TransportOption{
			// Dual mode (default) so the scenario's legacy initialize +
			// session-id handshake works, matching the TS PR 1624 SUT which
			// speaks the 2025-11-25 wire.
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

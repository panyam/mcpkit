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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/panyam/mcpkit/core"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

const (
	defaultKeycloakURL = "http://localhost:8180"
	defaultRealm       = "mcpkit-test"
	scopeToolsRead     = "tools-read"
	scopeToolsCall     = "tools-call"
	scopeAdminWrite    = "admin-write"
)

// discoverOIDC fetches Keycloak's openid-configuration document and returns
// the issuer + JWKS URI. Inline rather than depending on testutil's helper
// (test-only); ~10 lines and clearer about what we're hitting.
func discoverOIDC(realmURL string) (issuer, jwksURI string, err error) {
	url := realmURL + "/.well-known/openid-configuration"
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", "", fmt.Errorf("decode %s: %w", url, err)
	}
	return meta.Issuer, meta.JWKSURI, nil
}

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
	issuer, jwksURI, err := discoverOIDC(realmURL)
	if err != nil {
		log.Fatalf("OIDC discovery failed against %s: %v", realmURL, err)
	}

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
			// SEP-2575 stateless wire so the conformance scenario can hit
			// tools/call directly without an initialize handshake or
			// Mcp-Session-Id. Matches the wire mode PR 1624's reference
			// impl uses when sessionIdGenerator is undefined.
			server.WithStatelessMode(stateless.ModeStateless),
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

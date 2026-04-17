// Example: JWT/JWKS validation with claims propagation.
//
// Server validates RS256 JWTs via an in-process JWKS endpoint.
// Tool handlers read authenticated claims (subject, scopes).
// Demonstrates: JWTValidator, token minting, signature verification.
//
// Run: go run ./jwt
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/testutil"
)

func main() {
	fmt.Println("=== Auth Example: JWT/JWKS Validation ===")
	fmt.Println()

	// Step 1: Start in-process authorization server.
	// This provides: JWKS endpoint, token endpoint, RS256 key pair.
	as := testutil.NewTestAuthServerDirect(
		testutil.WithScopes([]string{"read", "write"}),
	)
	defer as.Close()
	fmt.Printf("Authorization server at %s\n", as.URL())
	fmt.Printf("  JWKS: %s\n", as.JWKSURL())
	fmt.Printf("  Issuer: %s\n\n", as.Issuer())

	// Step 2: Create MCP server with JWT validation.
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	// Set audience AFTER we know the URL.
	as.APIAuth.JWTAudience = ts.URL

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:  as.JWKSURL(),
		Issuer:   as.Issuer(),
		Audience: ts.URL,
	})
	validator.Start()
	defer validator.Stop()

	srv := server.NewServer(
		core.ServerInfo{Name: "jwt-demo", Version: "1.0"},
		server.WithAuth(validator),
	)

	// Tool that reports the authenticated user's identity.
	srv.Register(core.TextTool[struct{}]("whoami", "Reports the authenticated user's identity",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			claims := ctx.AuthClaims()
			if claims == nil {
				return "anonymous (no claims)", nil
			}
			data, _ := json.Marshal(map[string]any{
				"sub":    claims.Subject,
				"iss":    claims.Issuer,
				"scopes": claims.Scopes,
			})
			return string(data), nil
		},
	))

	handler = srv.Handler(server.WithStreamableHTTP(true))
	fmt.Printf("MCP server at %s (auth: JWT via JWKS)\n\n", ts.URL)

	// Step 3: Connect as "alice" with a valid token.
	fmt.Println("Step 1: Connect as alice with valid JWT...")
	token := mintToken(as, "alice", []string{"read", "write"})
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken(token),
	)
	if err := c.Connect(); err != nil {
		fmt.Printf("  → Error: %v\n", err)
		return
	}
	result, _ := c.ToolCall("whoami", nil)
	fmt.Printf("  → whoami: %s ✓\n\n", result)
	c.Close()

	// Step 4: Try a tampered token → 401.
	fmt.Println("Step 2: Connect with tampered token...")
	tampered := token[:len(token)-5] + "XXXXX"
	c2 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken(tampered),
	)
	if err := c2.Connect(); err != nil {
		fmt.Printf("  → Rejected (signature invalid): %v ✓\n", err)
	}

	fmt.Println("\n=== Done ===")
}

func mintToken(as *testutil.TestAuthServer, userID string, scopes []string) string {
	tok, err := as.MintTokenForSubject(userID, scopes)
	if err != nil {
		panic(err)
	}
	return tok
}

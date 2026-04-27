// Example: Fine-Grained Authorization Denial with scope step-up (UC2).
//
// Demonstrates the FineGrainedAuth UC2 pattern where a tool requires
// broader OAuth scopes than the client's current token provides. The
// server returns a structured authorization denial with remediation
// hints telling the client which scopes to request.
//
// Requires Keycloak (run "make upkcl" from the repo root).
//
// Flow:
//  1. Get a read-only token from Keycloak
//  2. read_document succeeds (only needs tools-read)
//  3. update_document fails with authorization denial + remediationHints
//  4. Get a broader token with tools-read + tools-call
//  5. Retry update_document — succeeds
//
// Run:
//
//	make run    # or: go run . -addr :8087
//
// The server prints tokens and a step-by-step walkthrough.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	oneauthcore "github.com/panyam/oneauth/core"
)

const (
	keycloakURL  = "http://localhost:8180"
	realmName    = "mcpkit-test"
	clientID     = "mcp-confidential"
	clientSecret = "mcp-test-secret-for-confidential"

	scopeRead = "tools-read"
	scopeCall = "tools-call"
)

func main() {
	addr := flag.String("addr", ":8087", "listen address")
	flag.Parse()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)

	// Discover Keycloak OIDC configuration.
	realmURL := keycloakURL + "/realms/" + realmName
	oidc, err := discoverOIDC(realmURL)
	if err != nil {
		log.Fatalf("Keycloak not reachable at %s — run 'make upkcl' first: %v", keycloakURL, err)
	}

	// JWT validator pointed at Keycloak's JWKS.
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             oidc.JWKSURI,
		Issuer:              oidc.Issuer,
		Audience:            "",
		ResourceMetadataURL: listenURL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           []string{scopeRead, scopeCall},
	})
	validator.Start()
	defer validator.Stop()

	srv := server.NewServer(
		core.ServerInfo{Name: "fine-grained-auth-example", Version: "1.0.0"},
		server.WithAuth(validator),
	)

	// read_document: requires tools-read scope (included in read-only tokens).
	srv.RegisterTool(
		core.ToolDef{
			Name:        "read_document",
			Description: "Read a document. Requires tools-read scope.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"docId": {"type": "string", "description": "Document ID"}
				}
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if err := auth.RequireScope(ctx, scopeRead); err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			var args struct {
				DocID string `json:"docId"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.DocID == "" {
				args.DocID = "doc-001"
			}
			return core.TextResult(fmt.Sprintf(
				"Document %q: Lorem ipsum dolor sit amet, consectetur adipiscing elit.", args.DocID)), nil
		},
	)

	// update_document: requires tools-call scope.
	// When the token lacks tools-call, returns a structured authorization denial
	// with remediationHints telling the client to re-authorize with broader scopes.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "update_document",
			Description: "Update a document. Requires tools-call scope (returns authorization denial with remediation hints if missing).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"docId":   {"type": "string", "description": "Document ID"},
					"content": {"type": "string", "description": "New content"}
				},
				"required": ["docId", "content"]
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if err := auth.RequireScope(ctx, scopeCall); err != nil {
				// Return authorization denial with remediation hints.
				// EXPERIMENTAL: The authorization denial envelope is from the
				// FineGrainedAuth proposal (draft SEP). Type names, field names,
				// and wire format are subject to breaking changes.
				denial := core.AuthorizationDenial{
					Reason: "insufficient_authorization", // EXPERIMENTAL: value will be standardized
					RemediationHints: []core.RemediationHint{
						core.ScopeStepUpHint([]string{scopeRead, scopeCall}),
					},
				}
				denialJSON, _ := json.Marshal(map[string]any{
					"error":         "insufficient_scope",
					"authorization": denial,
					"message":       fmt.Sprintf("Token lacks required scope %q. Re-authorize with scopes: %s %s", scopeCall, scopeRead, scopeCall),
				})
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: string(denialJSON)}},
					IsError: true,
				}, nil
			}

			var args struct {
				DocID   string `json:"docId"`
				Content string `json:"content"`
			}
			json.Unmarshal(req.Arguments, &args)
			return core.TextResult(fmt.Sprintf(
				"Document %q updated successfully.", args.DocID)), nil
		},
	)

	// initiate_payment: UC3 — per-operation ephemeral credential with RAR.
	//
	// EXPERIMENTAL: Demonstrates the FineGrainedAuth UC3 pattern where a payment
	// requires a separate ephemeral credential with RFC 9396 authorization_details
	// bound to the specific transaction. The original read token is retained.
	//
	// The client must obtain an ephemeral token with a payment_initiation
	// authorization_details type, then retry with that token. The original
	// token continues to be used for other operations.
	//
	// Note: Full end-to-end flow requires Keycloak with --features=rar enabled.
	// This example demonstrates the denial response shape and remediation hints.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "initiate_payment",
			Description: "Initiate a payment. Requires an ephemeral token with payment_initiation authorization_details (UC3 — per-operation credential).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"amount":   {"type": "string", "description": "Payment amount"},
					"currency": {"type": "string", "description": "Currency code (e.g., EUR, USD)"},
					"payee":    {"type": "string", "description": "Payee name"}
				},
				"required": ["amount", "currency", "payee"]
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
				Payee    string `json:"payee"`
			}
			json.Unmarshal(req.Arguments, &args)

			// In a real implementation, we'd check the token's authorization_details
			// for a matching payment_initiation entry. For this example, we always
			// return the denial to demonstrate the wire format.
			//
			// TODO: When ext/auth integrates oneauth RAR middleware, replace this
			// with auth.RequireAuthorizationDetails(ctx, "payment_initiation").

			// Build the RFC 9396 authorization_details for the payment.
			paymentDetail := oneauthcore.AuthorizationDetail{
				Type:    "payment_initiation",
				Actions: []string{"initiate", "status", "cancel"},
				Extra: map[string]any{
					"instructedAmount": map[string]any{
						"currency": args.Currency,
						"amount":   args.Amount,
					},
					"creditorName": args.Payee,
				},
			}

			// Return authorization denial with:
			//   - credential_disposition: "additional" (new token coexists with existing)
			//   - remediationHints with oauth_authorization_details containing the
			//     payment_initiation details the client needs to request
			//
			// EXPERIMENTAL: All field names and values are subject to breaking changes.
			denial := core.AuthorizationDenial{
				Reason:                "insufficient_authorization", // EXPERIMENTAL: value will be standardized
				CredentialDisposition: "additional",                 // EXPERIMENTAL: new credential coexists with existing
				RemediationHints: []core.RemediationHint{
					{
						Type: core.RemediationTypeOAuthRAR,
						Data: map[string]any{
							"authorization_details": []oneauthcore.AuthorizationDetail{paymentDetail},
						},
					},
				},
			}

			denialJSON, _ := json.Marshal(map[string]any{
				"error":         "insufficient_authorization",
				"authorization": denial,
				"message": fmt.Sprintf(
					"Payment of %s %s to %s requires a transaction-specific credential. "+
						"Obtain an ephemeral token with payment_initiation authorization_details.",
					args.Amount, args.Currency, args.Payee),
			})
			return core.ToolResult{
				Content: []core.Content{{Type: "text", Text: string(denialJSON)}},
				IsError: true,
			}, nil
		},
	)

	// Wire HTTP mux with PRM endpoints.
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          listenURL,
		AuthorizationServers: []string{oidc.Issuer},
		ScopesSupported:      []string{scopeRead, scopeCall},
		MCPPath:              "/mcp",
	})

	// Mint tokens for the walkthrough.
	tokRead := getToken(oidc.TokenEndpoint, scopeRead)
	tokReadCall := getToken(oidc.TokenEndpoint, scopeRead, scopeCall)

	log.Printf("Fine-Grained Auth example on %s", *addr)
	log.Printf("MCP endpoint: %s/mcp", listenURL)
	log.Printf("")
	log.Printf("Tokens (copy-paste into Authorization: Bearer <token>):")
	log.Printf("  read only:       %s", tokRead)
	log.Printf("  read+call:       %s", tokReadCall)
	log.Printf("")
	log.Printf("Exercises:")
	log.Printf("  1. Connect with read-only token, call read_document -> succeeds")
	log.Printf("  2. Call update_document -> fails with authorization denial + remediationHints")
	log.Printf("  3. Reconnect with read+call token, call update_document -> succeeds")
	log.Printf("  4. Call initiate_payment -> authorization denial with payment_initiation RAR details (UC3)")

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// --- OIDC discovery and token helpers (no test dependencies) ---

type oidcConfig struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

func discoverOIDC(realmURL string) (*oidcConfig, error) {
	resp, err := http.Get(realmURL + "/.well-known/openid-configuration")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OIDC discovery returned %d", resp.StatusCode)
	}
	var cfg oidcConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func getToken(tokenEndpoint string, scopes ...string) string {
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {strings.Join(scopes, " ")},
	}
	resp, err := http.PostForm(tokenEndpoint, data)
	if err != nil {
		log.Printf("WARNING: token request failed: %v", err)
		return "<token-unavailable>"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(body, &tok)
	if tok.AccessToken == "" {
		log.Printf("WARNING: no access_token in response: %s", body)
		return "<token-unavailable>"
	}
	return tok.AccessToken
}

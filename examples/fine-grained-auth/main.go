// Example: Fine-Grained Authorization Denial — scope step-up (UC2) + per-operation
// ephemeral credentials (UC3).
//
// Demonstrates the FineGrainedAuth UC2/UC3 patterns where a tool requires broader
// OAuth scopes or a transaction-specific credential. The server returns structured
// authorization denials with remediation hints.
//
// Requires Keycloak (run "make upkcl" from the repo root).
//
// Run modes:
//
//	go run .                 # interactive plain text
//	go run . --tui           # interactive with styled boxes
//	go run . --serve         # standalone MCP server on :8080
//	go run . --readme        # generate README.md
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
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
	// --serve mode: standalone MCP server for MCPJam / VS Code / Claude.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}

	demo := demokit.New("Fine-Grained Authorization — Scope Step-Up (UC2) + Ephemeral Credentials (UC3)").
		Dir("fine-grained-auth").
		Description("Demonstrates structured authorization denial with remediation hints. Requires Keycloak.").
		Actors(
			demokit.Actor("Client", "MCP Client"),
			demokit.Actor("Server", "MCP Server"),
			demokit.Actor("KC", "Keycloak"),
		)

	var (
		baseURL      string
		sessionID    string
		tokRead      string // read-only token
		tokReadCall  string // read + call token
	)

	// --- Step 1: Start server ---
	demo.Step("Start the MCP server with JWT auth + scope enforcement").
		Note("The server validates JWTs from Keycloak and enforces per-tool scope requirements. read_document needs tools-read; update_document needs tools-call.").
		Run(func() {
			addr, err := startServer()
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				fmt.Printf("    Is Keycloak running? Run 'make upkcl' from the repo root.\n")
				return
			}
			baseURL = "http://" + addr
			fmt.Printf("    Server started at %s\n", baseURL)
			fmt.Printf("    MCP endpoint: %s/mcp\n", baseURL)
		})

	// --- Step 2: Get read-only token ---
	demo.Step("Get a read-only token from Keycloak").
		Arrow("Client", "KC", "POST /token — client_credentials, scope=tools-read").
		DashedArrow("KC", "Client", "access_token (tools-read only)").
		Note("The client obtains a token with only the tools-read scope. This is sufficient for reading but not for writing.").
		Run(func() {
			realmURL := keycloakURL + "/realms/" + realmName
			oidc, err := discoverOIDC(realmURL)
			if err != nil {
				fmt.Printf("    ERROR: Keycloak not reachable: %v\n", err)
				return
			}
			tokRead = getToken(oidc.TokenEndpoint, scopeRead)
			fmt.Printf("    Token endpoint: %s\n", oidc.TokenEndpoint)
			fmt.Printf("    Scopes requested: %s\n", scopeRead)
			fmt.Printf("    Token: %s...%s\n", tokRead[:20], tokRead[len(tokRead)-10:])
		})

	// --- Step 3: Initialize session ---
	demo.Step("Initialize MCP session with read-only token").
		Arrow("Client", "Server", "POST /mcp — initialize + Bearer token").
		DashedArrow("Server", "Client", "serverInfo + Mcp-Session-Id").
		Note("The client connects with the read-only token. JWT validation passes — the token is valid, just limited in scope.").
		Run(func() {
			resp, err := mcpCall(baseURL, "", tokRead, "initialize", map[string]any{
				"protocolVersion": "2025-03-26",
				"clientInfo":      map[string]any{"name": "demo-client", "version": "1.0"},
				"capabilities":    map[string]any{},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			sessionID = resp.sessionID
			fmt.Printf("    Session ID: %s\n", sessionID)

			var result any
			json.Unmarshal(resp.result, &result)
			respJSON, _ := json.MarshalIndent(result, "    ", "  ")
			fmt.Printf("    Response:\n    %s\n", respJSON)

			mcpNotify(baseURL, sessionID, tokRead, "notifications/initialized", nil)
		})

	// --- Step 4: read_document succeeds ---
	demo.Step("Call read_document — succeeds with tools-read scope").
		Arrow("Client", "Server", "tools/call: read_document").
		DashedArrow("Server", "Client", "Document content").
		Note("The read_document tool only requires tools-read, which our token has. The call succeeds.").
		Run(func() {
			resp, err := mcpCall(baseURL, sessionID, tokRead, "tools/call", map[string]any{
				"name":      "read_document",
				"arguments": map[string]any{"docId": "doc-123"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if resp.rpcError != nil {
				fmt.Printf("    UNEXPECTED error: %d — %s\n", resp.rpcError.Code, resp.rpcError.Message)
				return
			}
			var result core.ToolResult
			json.Unmarshal(resp.result, &result)
			fmt.Printf("    Result: %s\n", result.Content[0].Text)
		})

	// --- Step 5: update_document fails (UC2) ---
	demo.Step("Call update_document — denied with remediationHints (UC2: scope step-up)").
		Arrow("Client", "Server", "tools/call: update_document").
		DashedArrow("Server", "Client", "Tool error + authorization denial + remediationHints").
		Note("The update_document tool requires tools-call scope, which our read-only token lacks. The server returns a structured authorization denial with remediation hints telling the client which scopes to request.").
		Run(func() {
			resp, err := mcpCall(baseURL, sessionID, tokRead, "tools/call", map[string]any{
				"name":      "update_document",
				"arguments": map[string]any{"docId": "doc-123", "content": "Updated content"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if resp.rpcError != nil {
				fmt.Printf("    RPC error: %d — %s\n", resp.rpcError.Code, resp.rpcError.Message)
				return
			}

			// The denial comes as a tool error result (isError: true).
			var result core.ToolResult
			json.Unmarshal(resp.result, &result)
			if !result.IsError {
				fmt.Printf("    UNEXPECTED: tool succeeded (expected authorization denial)\n")
				return
			}

			// Pretty-print the denial JSON from the text content.
			var denial any
			json.Unmarshal([]byte(result.Content[0].Text), &denial)
			denialJSON, _ := json.MarshalIndent(denial, "    ", "  ")
			fmt.Printf("    Authorization denial (isError: true):\n    %s\n", denialJSON)
		})

	// --- Step 6: Get broader token ---
	demo.Step("Get a broader token with tools-read + tools-call scopes").
		Arrow("Client", "KC", "POST /token — client_credentials, scope=tools-read tools-call").
		DashedArrow("KC", "Client", "access_token (tools-read + tools-call)").
		Note("Following the remediation hint, the client re-authorizes with the required scopes.").
		Run(func() {
			realmURL := keycloakURL + "/realms/" + realmName
			oidc, _ := discoverOIDC(realmURL)
			tokReadCall = getToken(oidc.TokenEndpoint, scopeRead, scopeCall)
			fmt.Printf("    Scopes requested: %s %s\n", scopeRead, scopeCall)
			fmt.Printf("    Token: %s...%s\n", tokReadCall[:20], tokReadCall[len(tokReadCall)-10:])
		})

	// --- Step 7: Retry update_document with new session ---
	demo.Step("Retry update_document with broader token — succeeds").
		Arrow("Client", "Server", "POST /mcp — initialize + Bearer (broader token)").
		DashedArrow("Server", "Client", "new session").
		Arrow("Client", "Server", "tools/call: update_document").
		DashedArrow("Server", "Client", "Document updated successfully").
		Note("The client starts a new session with the broader token. Now update_document succeeds because the token includes tools-call.").
		Run(func() {
			// New session with broader token.
			resp, err := mcpCall(baseURL, "", tokReadCall, "initialize", map[string]any{
				"protocolVersion": "2025-03-26",
				"clientInfo":      map[string]any{"name": "demo-client", "version": "1.0"},
				"capabilities":    map[string]any{},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			newSessionID := resp.sessionID
			mcpNotify(baseURL, newSessionID, tokReadCall, "notifications/initialized", nil)
			fmt.Printf("    New session: %s\n", newSessionID)

			// Retry update_document.
			resp, err = mcpCall(baseURL, newSessionID, tokReadCall, "tools/call", map[string]any{
				"name":      "update_document",
				"arguments": map[string]any{"docId": "doc-123", "content": "Updated content"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if resp.rpcError != nil {
				fmt.Printf("    ERROR: %d — %s\n", resp.rpcError.Code, resp.rpcError.Message)
				return
			}
			var result core.ToolResult
			json.Unmarshal(resp.result, &result)
			fmt.Printf("    Result: %s\n", result.Content[0].Text)
		})

	demo.Section("UC3: Per-Operation Ephemeral Credential",
		"UC3 demonstrates a different pattern: the client needs an *additional* token",
		"for a specific operation (payment), while retaining the original token for",
		"other operations. The server returns `credential_disposition: \"additional\"`",
		"and RFC 9396 `authorization_details` in the remediation hint.",
	)

	// --- Step 8: initiate_payment (UC3) ---
	demo.Step("Call initiate_payment — denied with RAR authorization_details (UC3)").
		Arrow("Client", "Server", "tools/call: initiate_payment").
		DashedArrow("Server", "Client", "Authorization denial + payment_initiation RAR + credential_disposition: additional").
		Note("The payment tool requires a transaction-specific ephemeral credential with RFC 9396 authorization_details. The denial tells the client exactly what to request from the authorization server.").
		Run(func() {
			resp, err := mcpCall(baseURL, sessionID, tokRead, "tools/call", map[string]any{
				"name": "initiate_payment",
				"arguments": map[string]any{
					"amount":   "150.00",
					"currency": "EUR",
					"payee":    "ACME Corp",
				},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if resp.rpcError != nil {
				fmt.Printf("    RPC error: %d — %s\n", resp.rpcError.Code, resp.rpcError.Message)
				return
			}

			var result core.ToolResult
			json.Unmarshal(resp.result, &result)

			// Pretty-print the denial JSON.
			var denial any
			json.Unmarshal([]byte(result.Content[0].Text), &denial)
			denialJSON, _ := json.MarshalIndent(denial, "    ", "  ")
			fmt.Printf("    Authorization denial (UC3 — ephemeral credential):\n    %s\n", denialJSON)
		})

	// Use TUI renderer if --tui flag is passed.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()
}

// --- MCP HTTP helpers ---

type mcpResponse struct {
	sessionID string
	result    json.RawMessage
	rpcError  *core.Error
}

func mcpCall(baseURL, sessionID, token, method string, params any) (*mcpResponse, error) {
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	body, _ := json.Marshal(rpcReq)

	req, _ := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Handle HTTP-level auth errors (401/403).
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s (WWW-Authenticate: %s)",
			resp.StatusCode, strings.TrimSpace(string(body)), resp.Header.Get("WWW-Authenticate"))
	}

	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		sid = sessionID
	}

	raw, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var rpcResp struct {
			Result json.RawMessage `json:"result,omitempty"`
			Error  *core.Error     `json:"error,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &rpcResp); err != nil {
			continue
		}
		return &mcpResponse{
			sessionID: sid,
			result:    rpcResp.Result,
			rpcError:  rpcResp.Error,
		}, nil
	}
	return nil, fmt.Errorf("no JSON-RPC response in SSE stream")
}

func mcpNotify(baseURL, sessionID, token, method string, params any) {
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		rpcReq["params"] = params
	}
	body, _ := json.Marshal(rpcReq)
	req, _ := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// --- Server setup ---

func startServer() (string, error) {
	realmURL := keycloakURL + "/realms/" + realmName
	oidc, err := discoverOIDC(realmURL)
	if err != nil {
		return "", fmt.Errorf("Keycloak not reachable at %s: %w", keycloakURL, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	listenURL := "http://" + addr

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             oidc.JWKSURI,
		Issuer:              oidc.Issuer,
		Audience:            "",
		ResourceMetadataURL: listenURL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           []string{scopeRead, scopeCall},
	})
	validator.Start()

	srv := server.NewServer(
		core.ServerInfo{Name: "fine-grained-auth-example", Version: "1.0.0"},
		server.WithAuth(validator),
	)

	registerTools(srv)

	mux := http.NewServeMux()
	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)
	mux.Handle("/mcp", cors(srv.Handler(server.WithStreamableHTTP(true))))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          listenURL,
		AuthorizationServers: []string{oidc.Issuer},
		ScopesSupported:      []string{scopeRead, scopeCall},
		MCPPath:              "/mcp",
	})

	go http.Serve(ln, mux)
	return addr, nil
}

// serve starts the MCP server standalone (no demokit).
func serve() {
	addr := ":8080"
	for i, arg := range os.Args[1:] {
		if arg == "--addr" && i+2 < len(os.Args) {
			addr = os.Args[i+2]
		}
	}
	listenURL := fmt.Sprintf("http://localhost%s", addr)

	realmURL := keycloakURL + "/realms/" + realmName
	oidc, err := discoverOIDC(realmURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Keycloak not reachable at %s — run 'make upkcl' first: %v\n", keycloakURL, err)
		os.Exit(1)
	}

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

	registerTools(srv)

	mux := http.NewServeMux()
	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)
	mux.Handle("/mcp", cors(srv.Handler(server.WithStreamableHTTP(true))))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          listenURL,
		AuthorizationServers: []string{oidc.Issuer},
		ScopesSupported:      []string{scopeRead, scopeCall},
		MCPPath:              "/mcp",
	})

	// Mint tokens for the user.
	tokRead := getToken(oidc.TokenEndpoint, scopeRead)
	tokReadCall := getToken(oidc.TokenEndpoint, scopeRead, scopeCall)

	fmt.Printf("Fine-Grained Auth server on %s\n", addr)
	fmt.Printf("MCP endpoint: %s/mcp\n", listenURL)
	fmt.Printf("\nTokens (paste into Authorization: Bearer <token>):\n")
	fmt.Printf("  read only:   %s\n", tokRead)
	fmt.Printf("  read+call:   %s\n", tokReadCall)
	fmt.Printf("\nTools: read_document, update_document, initiate_payment\n")

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

// --- Shared tool registration ---

func registerTools(srv *server.Server) {
	// read_document: requires tools-read scope.
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
				denial := core.AuthorizationDenial{
					Reason: "insufficient_authorization",
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
	srv.RegisterTool(
		core.ToolDef{
			Name:        "initiate_payment",
			Description: "Initiate a payment. Requires an ephemeral token with payment_initiation authorization_details (UC3).",
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

			denial := core.AuthorizationDenial{
				Reason:               "insufficient_authorization",
				CredentialDisposition: "additional",
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
}

// --- OIDC discovery and token helpers ---

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
		return "<token-unavailable>"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(body, &tok)
	if tok.AccessToken == "" {
		return "<token-unavailable>"
	}
	return tok.AccessToken
}

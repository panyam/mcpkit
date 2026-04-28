// Example: Fine-Grained Authorization Denial — scope step-up (UC2) + per-operation
// ephemeral credentials (UC3).
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # starts the MCP server on :8080
//	Terminal 2:  make run           # runs the demokit client (scripted MCP host)
//
// The server is a real MCP server that any host can connect to.
// The demokit client acts as a scripted host walking through UC2/UC3 flows.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
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
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}

	runDemo()
}

// --- Demo client (scripted MCP host) ---

func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}

	demo := demokit.New("Fine-Grained Authorization — Scope Step-Up (UC2) + Ephemeral Credentials (UC3)").
		Dir("fine-grained-auth").
		Description("A scripted MCP host walking through UC2/UC3 authorization denial flows.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("KC", "Keycloak"),
		)

	demo.Section("Setup",
		"Before running this demo, start the MCP server and Keycloak in separate terminals:",
		"",
		"```",
		"Terminal 1:  make kcl          # start Keycloak (if not running)",
		"Terminal 2:  make serve        # start the MCP server on :8080",
		"Terminal 3:  make run          # run this demo",
		"```",
	)

	var (
		readClient *client.Client // client with read-only token
		callClient *client.Client // client with read+call token
		tokRead    string
		tokReadCall string
	)

	// --- Step 1: Get read-only token ---
	demo.Step("Get a read-only token from Keycloak (scope: tools-read)").
		Arrow("Host", "KC", "POST /token — client_credentials, scope=tools-read").
		DashedArrow("KC", "Host", "access_token (tools-read only)").
		Note("The host obtains a token with only the tools-read scope. This is sufficient for reading but not for writing or payments.").
		Run(func() {
			realmURL := keycloakURL + "/realms/" + realmName
			oidc, err := discoverOIDC(realmURL)
			if err != nil {
				fmt.Printf("    ERROR: Keycloak not reachable: %v\n", err)
				fmt.Printf("    Start it with: make kcl\n")
				return
			}
			tokRead = getToken(oidc.TokenEndpoint, scopeRead)
			fmt.Printf("    Token endpoint: %s\n", oidc.TokenEndpoint)
			fmt.Printf("    Scopes requested: %s\n", scopeRead)
			fmt.Printf("    Token: %s...%s\n", tokRead[:min(20, len(tokRead))], tokRead[max(0, len(tokRead)-10):])
		})

	// --- Step 2: Connect with read-only token ---
	demo.Step("Connect to MCP server with read-only token").
		Arrow("Host", "Server", "POST /mcp — initialize + Authorization: Bearer <read-token>").
		DashedArrow("Server", "Host", "serverInfo + Mcp-Session-Id").
		Note("The host connects with the read-only token. JWT validation passes — the token is valid, just limited in scope.").
		Run(func() {
			fmt.Printf("    Connecting to %s ...\n", serverURL)
			readClient = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithClientBearerToken(tokRead),
			)
			if err := readClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				fmt.Printf("    Start the server with: make serve\n")
				return
			}
			fmt.Printf("    Connected to %s %s\n\n", readClient.ServerInfo.Name, readClient.ServerInfo.Version)

			tools, _ := readClient.ListTools()
			fmt.Printf("    Tools:\n")
			for _, t := range tools {
				fmt.Printf("      - %s: %s\n", t.Name, t.Description)
			}
		})

	// --- Step 3: read_document succeeds ---
	demo.Step("Call read_document — succeeds (tools-read is sufficient)").
		Arrow("Host", "Server", "tools/call: read_document {docId: \"doc-123\"}").
		DashedArrow("Server", "Host", "Document content").
		Note("The read_document tool only requires tools-read scope. Our token has it, so the call succeeds.").
		Run(func() {
			text, err := readClient.ToolCall("read_document", map[string]any{"docId": "doc-123"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
		})

	// --- Step 4: update_document fails (UC2) ---
	demo.Step("Call update_document — DENIED (UC2: needs tools-call scope)").
		Arrow("Host", "Server", "tools/call: update_document {docId: \"doc-123\"}").
		DashedArrow("Server", "Host", "isError: true + authorization denial + remediationHints").
		Note("The update_document tool requires tools-call scope. Our read-only token lacks it. The server returns a structured authorization denial telling the host exactly which scopes to request.").
		Run(func() {
			result, err := readClient.ToolCallFull("update_document", map[string]any{
				"docId": "doc-123", "content": "Updated content",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if !result.IsError {
				fmt.Printf("    UNEXPECTED: tool succeeded (expected denial)\n")
				return
			}

			// The denial is JSON inside the text content.
			var denial any
			json.Unmarshal([]byte(result.Content[0].Text), &denial)
			denialJSON, _ := json.MarshalIndent(denial, "    ", "  ")
			fmt.Printf("    Tool returned isError: true\n")
			fmt.Printf("    Authorization denial:\n    %s\n\n", denialJSON)
			fmt.Printf("    The remediationHints tell the host to re-authorize with scopes:\n")
			fmt.Printf("      tools-read + tools-call\n")
		})

	// --- Step 5: Get broader token ---
	demo.Step("Re-authorize: get a broader token (tools-read + tools-call)").
		Arrow("Host", "KC", "POST /token — client_credentials, scope=tools-read tools-call").
		DashedArrow("KC", "Host", "access_token (tools-read + tools-call)").
		Note("A smart host reads the remediationHints and automatically requests the required scopes. Here we simulate that by getting a new token.").
		Run(func() {
			realmURL := keycloakURL + "/realms/" + realmName
			oidc, _ := discoverOIDC(realmURL)
			tokReadCall = getToken(oidc.TokenEndpoint, scopeRead, scopeCall)
			fmt.Printf("    Scopes requested: %s %s\n", scopeRead, scopeCall)
			fmt.Printf("    New token: %s...%s\n", tokReadCall[:min(20, len(tokReadCall))], tokReadCall[max(0, len(tokReadCall)-10):])
		})

	// --- Step 6: Retry with broader token ---
	demo.Step("Retry update_document with broader token — SUCCEEDS").
		Arrow("Host", "Server", "POST /mcp — initialize + Bearer (broader token)").
		DashedArrow("Server", "Host", "new session").
		Arrow("Host", "Server", "tools/call: update_document").
		DashedArrow("Server", "Host", "Document updated successfully").
		Note("The host starts a new session with the broader token. Now update_document succeeds because the token includes tools-call.").
		Run(func() {
			callClient = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithClientBearerToken(tokReadCall),
			)
			if err := callClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    New session connected\n\n")

			text, err := callClient.ToolCall("update_document", map[string]any{
				"docId": "doc-123", "content": "Updated content",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
		})

	demo.Section("UC3: Per-Operation Ephemeral Credential",
		"UC3 is a different pattern: the host needs an *additional* token for a",
		"specific operation (payment), while keeping the original token for other",
		"operations. The server returns credential_disposition: \"additional\" and",
		"RFC 9396 authorization_details in the remediation hint.",
	)

	// --- Step 7: initiate_payment (UC3) ---
	demo.Step("Call initiate_payment — DENIED with RAR authorization_details (UC3)").
		Arrow("Host", "Server", "tools/call: initiate_payment {amount: 150, currency: EUR, payee: ACME}").
		DashedArrow("Server", "Host", "isError: true + credential_disposition: additional + payment_initiation RAR").
		Note("The payment tool requires a transaction-specific ephemeral credential. The denial includes RFC 9396 authorization_details the host should use to request an additional token — the original token is kept for other operations.").
		Run(func() {
			result, err := readClient.ToolCallFull("initiate_payment", map[string]any{
				"amount":   "150.00",
				"currency": "EUR",
				"payee":    "ACME Corp",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			var denial any
			json.Unmarshal([]byte(result.Content[0].Text), &denial)
			denialJSON, _ := json.MarshalIndent(denial, "    ", "  ")
			fmt.Printf("    Authorization denial (UC3 — ephemeral credential):\n    %s\n\n", denialJSON)
			fmt.Printf("    Key differences from UC2:\n")
			fmt.Printf("      - credential_disposition: \"additional\" (don't replace the original token)\n")
			fmt.Printf("      - remediationHints type: oauth_authorization_details (RFC 9396 RAR)\n")
			fmt.Printf("      - The authorization_details are bound to this specific transaction\n")
		})

	// Use TUI renderer if --tui flag is passed.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()

	if readClient != nil {
		readClient.Close()
	}
	if callClient != nil {
		callClient.Close()
	}
}

// --- Serve mode: standalone MCP server ---

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
		fmt.Fprintf(os.Stderr, "ERROR: Keycloak not reachable at %s — run 'make kcl' first: %v\n", keycloakURL, err)
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

	logger := demokit.NewColorLogger("[mcp] ", []demokit.ColorRule{
		{Contains: "error=", DarkColor: demokit.ANSIRed},
		{Contains: "ERROR", DarkColor: demokit.ANSIRed},
		{Contains: "[http] →", DarkColor: demokit.ANSIDimCyan, LightColor: demokit.ANSIDimBlue},
		{Contains: "[http] ←", DarkColor: demokit.ANSICyan, LightColor: demokit.ANSIBlue},
		{Contains: "MCP ", DarkColor: demokit.ANSIGreen},
	})
	srv := server.NewServer(
		core.ServerInfo{Name: "fine-grained-auth-example", Version: "1.0.0"},
		server.WithAuth(validator),
		server.WithRequestLogging(logger),
		server.WithMiddleware(server.LoggingMiddleware(logger)),
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

	// Mint tokens for manual testing.
	tokRead := getToken(oidc.TokenEndpoint, scopeRead)
	tokReadCall := getToken(oidc.TokenEndpoint, scopeRead, scopeCall)

	fmt.Printf("Fine-Grained Auth server on %s\n", addr)
	fmt.Printf("MCP endpoint: %s/mcp\n\n", listenURL)
	fmt.Printf("Tokens (paste into Authorization: Bearer <token>):\n")
	fmt.Printf("  read only:   %s\n", tokRead)
	fmt.Printf("  read+call:   %s\n\n", tokReadCall)
	fmt.Printf("Tools:\n")
	fmt.Printf("  read_document      — needs tools-read\n")
	fmt.Printf("  update_document    — needs tools-call (returns denial if missing)\n")
	fmt.Printf("  initiate_payment   — always returns UC3 denial with RAR details\n")

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

// --- Shared tool registration ---

func registerTools(srv *server.Server) {
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

	srv.RegisterTool(
		core.ToolDef{
			Name:        "update_document",
			Description: "Update a document. Requires tools-call scope (returns authorization denial if missing).",
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


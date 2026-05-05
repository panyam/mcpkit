// Example: Fine-Grained Authorization (SEP-2643).
//
// Demonstrates UC2 (scope step-up) and UC3 (per-operation ephemeral
// credential / RFC 9396 RAR) end-to-end against an in-process oneauth AS.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # starts the MCP server + in-process AS
//	Terminal 2:  make run           # runs the demokit client (scripted MCP host)
//
// The MCP server runs an in-process oneauth AS (HTTPtest server in a goroutine)
// because Keycloak does not support RFC 9396 RAR. Tokens are RS256-signed; the
// MCP server validates them via the AS's JWKS endpoint.
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/admin"
	"github.com/panyam/oneauth/apiauth"
	oacore "github.com/panyam/oneauth/core"
	"github.com/panyam/oneauth/keys"
	"github.com/panyam/oneauth/utils"
	"github.com/panyam/servicekit/middleware"
)

const (
	scopeRead = "tools-read"
	scopeCall = "tools-call"

	// asKeyID is the keystore client_id under which we register the AS's
	// own keypair. It's not a real client — just a way to get the AS's key
	// published via the JWKSHandler.
	asKeyID = "_as_signer"
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
	serverURL := common.ServerURL()

	demo := demokit.New("Fine-Grained Authorization — Scope Step-Up (UC2) + Ephemeral Credentials (UC3)").
		Dir("fine-grained-auth").
		Description("**EXPERIMENTAL** — Tracks SEP-2643 (Structured Authorization Denials), currently a draft. UC2 + UC3 demonstrated end-to-end against an in-process oneauth AS.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("AS", "Auth Server (in-process oneauth)"),
		)

	demo.Section("Setup",
		"This example uses an in-process oneauth Authorization Server because",
		"Keycloak does not support RFC 9396 Rich Authorization Requests (RAR).",
		"The MCP server starts the AS in a goroutine, registers a confidential",
		"client for the demo, and exposes the client_id+secret + AS URL for the",
		"host to discover.",
		"",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve        # MCP server + in-process AS on :8080",
		"Terminal 2:  make run          # this demo",
		"```",
	)

	demo.Section("UC1 vs UC2/UC3 — When does the host react?",
		"UC1 (elicitation): the denial points to an out-of-band action (user clicks Approve).",
		"The host can't proceed until it receives `notifications/elicitation/complete`.",
		"",
		"UC2/UC3: the denial is a transport-level signal — UC2 is HTTP 403 + WWW-Authenticate,",
		"UC3 is a JSON-RPC error with an RFC 9396 remediationHint. The host parses it and reacts",
		"immediately by re-authorizing — no user interaction in this demo (a real banking host",
		"would prompt the user to confirm the payment first).",
	)

	var (
		readClient  *client.Client // client with read-only token
		callClient  *client.Client // client with broader token (after step-up)
		payClient   *client.Client // client with RAR-bound payment token (UC3)
		bootstrap   bootstrapInfo
		tokRead     string
		tokReadCall string
		tokPayment  string
		// captured from the UC2 denial — drives auto-step-up
		requiredScopes []string
		// captured from the UC3 denial — drives RAR token request
		paymentAuthzDetails []map[string]any
	)

	// --- Step 1: Discover AS bootstrap from server ---
	demo.Step("Discover the in-process AS bootstrap from the MCP server").
		Arrow("Host", "Server", "GET /demo/bootstrap").
		DashedArrow("Server", "Host", "{as_url, client_id, client_secret}").
		Note("The MCP server exposes a non-standard bootstrap endpoint that hands the host the in-process AS URL and a pre-registered client credential. In production, the host would do OAuth Dynamic Client Registration; this shortcut keeps the demo focused on SEP-2643.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			resp, err := http.Get(serverURL + "/demo/bootstrap")
			if err != nil {
				fmt.Printf("    ERROR: server not reachable at %s: %v\n", serverURL, err)
				fmt.Printf("    Start it with: make serve\n")
				return
			}
			defer resp.Body.Close()
			if err := json.NewDecoder(resp.Body).Decode(&bootstrap); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    AS URL:        %s\n", bootstrap.ASURL)
			fmt.Printf("    Token endpt:   %s\n", bootstrap.TokenEndpoint)
			fmt.Printf("    JWKS endpt:    %s\n", bootstrap.JWKSURL)
			fmt.Printf("    client_id:     %s\n", bootstrap.ClientID)
			fmt.Printf("    client_secret: %s\n", bootstrap.ClientSecret)
			return nil
		})

	// --- Step 2: Get read-only token ---
	demo.Step("Get a read-only token (scope: tools-read)").
		Arrow("Host", "AS", "POST /token — grant_type=client_credentials, scope=tools-read").
		DashedArrow("AS", "Host", "access_token (tools-read only)").
		Note("Standard OAuth 2.0 client_credentials grant. The token is RS256-signed by the AS and can be validated against the AS's JWKS endpoint.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			tok, err := requestToken(bootstrap, []string{scopeRead}, nil)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			tokRead = tok
			fmt.Printf("    Scopes requested: %s\n", scopeRead)
			fmt.Printf("    Token: %s...%s\n", tokRead[:min(20, len(tokRead))], tokRead[max(0, len(tokRead)-10):])
			return nil
		})

	// --- Step 3: Connect with read-only token ---
	demo.Step("Connect to MCP server with read-only token").
		Arrow("Host", "Server", "POST /mcp — initialize + Authorization: Bearer <read-token>").
		DashedArrow("Server", "Host", "serverInfo + Mcp-Session-Id").
		Note("JWT validation against the AS's JWKS endpoint succeeds — token is valid, just limited in scope.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			fmt.Printf("    Connecting to %s ...\n", serverURL)
			readClient = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithClientBearerToken(tokRead),
			)
			if err := readClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n\n", readClient.ServerInfo.Name, readClient.ServerInfo.Version)

			tools, _ := readClient.ListTools()
			fmt.Printf("    Tools:\n")
			for _, t := range tools {
				fmt.Printf("      - %s: %s\n", t.Name, t.Description)
			}
			return nil
		})

	// --- Step 4: read_document succeeds ---
	demo.Step("Call read_document — succeeds (tools-read is sufficient)").
		Arrow("Host", "Server", "tools/call: read_document {docId: \"doc-123\"}").
		DashedArrow("Server", "Host", "Document content").
		Note("The read_document tool only requires tools-read scope. Our token has it, so the call succeeds.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			text, err := readClient.ToolCall("read_document", map[string]any{"docId": "doc-123"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			return nil
		})

	// --- Step 5: update_document → HTTP 403 + WWW-Authenticate (UC2 spec-correct) ---
	demo.Step("Call update_document — DENIED (UC2: HTTP 403 + WWW-Authenticate)").
		Arrow("Host", "Server", "tools/call: update_document {docId: \"doc-123\"}").
		DashedArrow("Server", "Host", "HTTP 403 + WWW-Authenticate: Bearer error=\"insufficient_scope\", scope=\"tools-call\"").
		Note("Per SEP-2643 (FineGrainedAuth UC2): the server's auth.NewToolScopeMiddleware returns HTTP 403 with WWW-Authenticate before the handler runs. The mcpkit client surfaces this as *client.ClientAuthError with the required scopes already parsed from the header (RFC 6750).").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			_, err := readClient.ToolCallFull("update_document", map[string]any{
				"docId": "doc-123", "content": "Updated content",
			})
			if err == nil {
				fmt.Printf("    UNEXPECTED: tool succeeded (expected 403)\n")
				return
			}

			var authErr *client.ClientAuthError
			if !errors.As(err, &authErr) {
				fmt.Printf("    UNEXPECTED error type: %T %v\n", err, err)
				return
			}

			fmt.Printf("    HTTP %d response\n", authErr.StatusCode)
			fmt.Printf("    WWW-Authenticate: %s\n", authErr.WWWAuthenticate)
			fmt.Printf("    → RequiredScopes (parsed from header per RFC 6750): %v\n",
				authErr.RequiredScopes)
			requiredScopes = authErr.RequiredScopes
			return nil
		})

	// --- Step 6: Auto-step-up using scopes from WWW-Authenticate ---
	demo.Step("Auto-step-up: re-authorize with scopes from WWW-Authenticate").
		Arrow("Host", "AS", "POST /token — scope=<from WWW-Authenticate>").
		DashedArrow("AS", "Host", "access_token with broader scopes").
		Note("Spec-driven smart-host behavior: the WWW-Authenticate header named the required scopes; the host complies. We also re-include tools-read so the broader token works for both reads and writes (typical OAuth step-up: ask for the union, not a replacement).").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if len(requiredScopes) == 0 {
				fmt.Printf("    ERROR: no requiredScopes from previous step\n")
				return
			}
			scopes := append([]string{scopeRead}, requiredScopes...)
			tok, err := requestToken(bootstrap, scopes, nil)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			tokReadCall = tok
			fmt.Printf("    Scopes requested: %v\n", scopes)
			fmt.Printf("    New token: %s...%s\n", tokReadCall[:min(20, len(tokReadCall))], tokReadCall[max(0, len(tokReadCall)-10):])
			return nil
		})

	// --- Step 7: Retry with broader token ---
	demo.Step("Retry update_document with broader token — SUCCEEDS").
		Arrow("Host", "Server", "POST /mcp — initialize + Bearer (broader token)").
		DashedArrow("Server", "Host", "new session").
		Arrow("Host", "Server", "tools/call: update_document").
		DashedArrow("Server", "Host", "Document updated successfully").
		Note("New session with the broader token. update_document succeeds because the token includes tools-call.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
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
			return nil
		})

	demo.Section("UC3: Per-Operation Ephemeral Credential",
		"UC3 is a different pattern: the host needs an *additional* token for a",
		"specific operation (payment), while keeping the original token for other",
		"operations. The denial carries credentialDisposition: \"additional\" and",
		"RFC 9396 authorization_details bound to the specific transaction.",
	)

	// --- Step 8: initiate_payment → DENIED with RAR remediation ---
	demo.Step("Call initiate_payment — DENIED with RAR authorization_details (UC3)").
		Arrow("Host", "Server", "tools/call: initiate_payment {amount: 150 EUR, payee: ACME}").
		DashedArrow("Server", "Host", "JSON-RPC error + credentialDisposition: additional + payment_initiation RAR").
		Note("The payment tool requires a transaction-specific ephemeral credential. Our broader token has tools-call but no authorization_details bound to this payment, so the server returns the SEP-2643 envelope with an oauth_authorization_details remediationHint describing the exact authorization the host must request.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			_, err := callClient.ToolCall("initiate_payment", map[string]any{
				"amount":   "150.00",
				"currency": "EUR",
				"payee":    "ACME Corp",
			})
			if err == nil {
				fmt.Printf("    UNEXPECTED: tool succeeded (expected denial)\n")
				return nil
			}

			common.PrintRPCError(err, "")

			var rpcErr *client.RPCError
			if !errors.As(err, &rpcErr) {
				return nil
			}

			// Parse the SEP-2643 envelope.
			raw, _ := json.Marshal(rpcErr.Data)
			var parsed struct {
				Authorization struct {
					CredentialDisposition string `json:"credentialDisposition"`
					RemediationHints      []struct {
						Type                 string           `json:"type"`
						AuthorizationDetails []map[string]any `json:"authorization_details"`
					} `json:"remediationHints"`
				} `json:"authorization"`
			}
			json.Unmarshal(raw, &parsed)

			fmt.Printf("    → credentialDisposition: %q (additional = keep original token)\n",
				parsed.Authorization.CredentialDisposition)
			for _, h := range parsed.Authorization.RemediationHints {
				if h.Type == "oauth_authorization_details" {
					paymentAuthzDetails = h.AuthorizationDetails
					adJSON, _ := json.MarshalIndent(h.AuthorizationDetails, "    ", "  ")
					fmt.Printf("    → authorization_details (RFC 9396):\n    %s\n", adJSON)
				}
			}
			return nil
		})

	// --- Step 9: Request RAR-bound token ---
	demo.Step("Request an RAR-bound payment token from the AS").
		Arrow("Host", "AS", "POST /token — authorization_details=[payment_initiation, ...]").
		DashedArrow("AS", "Host", "access_token with authorization_details claim").
		Note("The host uses the authorization_details from the remediationHint *verbatim* in the OAuth token request (RFC 9396). The AS validates and embeds the authorization_details into the JWT as a claim. The host now holds two tokens: the original tools-read+tools-call token (for everything else) and this short-lived payment-bound token.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if len(paymentAuthzDetails) == 0 {
				fmt.Printf("    ERROR: no authorization_details from previous step\n")
				return
			}
			tok, err := requestToken(bootstrap, nil, paymentAuthzDetails)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			tokPayment = tok
			fmt.Printf("    New token: %s...%s\n", tokPayment[:min(20, len(tokPayment))], tokPayment[max(0, len(tokPayment)-10):])
			return nil
		})

	// --- Step 10: Retry with RAR-bound token — succeeds ---
	demo.Step("Retry initiate_payment with RAR-bound token — SUCCEEDS").
		Arrow("Host", "Server", "POST /mcp — initialize + Bearer (RAR token)").
		DashedArrow("Server", "Host", "new session").
		Arrow("Host", "Server", "tools/call: initiate_payment {amount: 150 EUR, payee: ACME}").
		DashedArrow("Server", "Host", "Payment initiated").
		Note("The server's initiate_payment handler reads authorization_details from the JWT claims and validates that a payment_initiation entry matches the request (amount, currency, payee). It does — the host minted exactly the token the server asked for in the previous denial.").
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			payClient = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithClientBearerToken(tokPayment),
			)
			if err := payClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    New session with RAR token connected\n\n")

			text, err := payClient.ToolCall("initiate_payment", map[string]any{
				"amount":   "150.00",
				"currency": "EUR",
				"payee":    "ACME Corp",
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			fmt.Printf("\n    Note: the original tokReadCall is unchanged and still valid for non-payment ops.\n")
			return nil
		})

	common.SetupRenderer(demo)

	demo.Execute()

	if readClient != nil {
		readClient.Close()
	}
	if callClient != nil {
		callClient.Close()
	}
	if payClient != nil {
		payClient.Close()
	}
}

// --- Bootstrap protocol (non-standard demo helper) ---

type bootstrapInfo struct {
	ASURL         string `json:"as_url"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURL       string `json:"jwks_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
}

// requestToken performs a client_credentials grant against the in-process AS.
// Pass scopes for UC2 (broader scope) or authorizationDetails for UC3 (RAR).
func requestToken(b bootstrapInfo, scopes []string, authzDetails []map[string]any) (string, error) {
	body := map[string]any{
		"grant_type":    "client_credentials",
		"client_id":     b.ClientID,
		"client_secret": b.ClientSecret,
	}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	if len(authzDetails) > 0 {
		body["authorization_details"] = authzDetails
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(b.TokenEndpoint, "application/json", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, respBody)
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(respBody, &tokResp); err != nil {
		return "", err
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response: %s", respBody)
	}
	return tokResp.AccessToken, nil
}

// --- Serve mode: standalone MCP server with in-process AS ---

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	// Canonical 5 rules + two example-specific tints (isError on tool results,
	// tool= on dispatch logs). Passed as variadic extras to NewMCPLogger.
	logger := common.NewMCPLogger("[mcp] ",
		demokit.ColorRule{Contains: "isError=true", DarkColor: demokit.ANSIBrightYellow, LightColor: demokit.ANSIYellow},
		demokit.ColorRule{Contains: "tool=", DarkColor: demokit.ANSIBrightCyan, LightColor: demokit.ANSIBlue},
	)

	// 1. Spin up the in-process oneauth AS.
	asInfo, err := startInProcessAS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to start in-process AS: %v\n", err)
		os.Exit(1)
	}
	defer asInfo.cleanup()

	// 2. Seed the document store in a per-instance temp dir.
	if err := seedDocs(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(docDir)

	// 3. mcpkit JWT validator pointed at the AS's JWKS endpoint.
	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             asInfo.JWKSURL,
		Issuer:              asInfo.ASURL,
		Audience:            "",
		ResourceMetadataURL: listenURL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           []string{scopeRead, scopeCall},
	})
	validator.Start()
	defer validator.Stop()

	// 3. MCP server with auth + scope-enforcement middleware.
	opts := []server.Option{server.WithListen(*addr)}
	opts = append(opts, common.WithMCPLogging(logger)...)
	opts = append(opts,
		server.WithAuth(validator),
		server.WithMiddleware(server.ToolCallLogger(logger)),
	)
	srv := server.NewServer(
		core.ServerInfo{Name: "fine-grained-auth-example", Version: "1.0.0"},
		opts...,
	)
	registerTools(srv)
	srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry()))
	srv.UseMiddleware(paymentAuthorizationMiddleware())

	// 4. CORS for browser-based MCP hosts; applied via WithHandlerWrap so
	// it covers /mcp + the auth.MountAuth routes + /demo/bootstrap uniformly.
	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	fmt.Printf("Fine-Grained Auth server on %s\n", *addr)
	fmt.Printf("MCP endpoint:   %s/mcp\n", listenURL)
	fmt.Printf("AS URL:         %s\n", asInfo.ASURL)
	fmt.Printf("AS JWKS:        %s\n", asInfo.JWKSURL)
	fmt.Printf("AS token endpt: %s\n", asInfo.TokenEndpoint)
	fmt.Printf("Demo client_id: %s\n", asInfo.ClientID)
	fmt.Printf("Demo secret:    %s\n", asInfo.ClientSecret)
	fmt.Printf("Doc store:      %s (cleaned up on graceful shutdown)\n", docDir)
	fmt.Printf("\nTools:\n")
	fmt.Printf("  read_document      — reads from doc store; needs tools-read scope\n")
	fmt.Printf("  update_document    — writes to doc store; needs tools-call scope\n")
	fmt.Printf("  initiate_payment   — needs payment_initiation authorization_details\n")
	fmt.Printf("\nSeed docs: doc-001, doc-123, doc-456\n")
	fmt.Printf("Inspect:   ls %s/ ; cat %s/doc-123.txt\n", docDir, docDir)

	if err := srv.ListenAndServe(
		server.WithStreamableHTTP(true),
		server.WithMux(func(mux *http.ServeMux) {
			auth.MountAuth(mux, auth.AuthConfig{
				ResourceURI:          listenURL,
				AuthorizationServers: []string{asInfo.ASURL},
				ScopesSupported:      []string{scopeRead, scopeCall},
				MCPPath:              "/mcp",
			})
			mux.HandleFunc("GET /demo/bootstrap", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(bootstrapInfo{
					ASURL:         asInfo.ASURL,
					TokenEndpoint: asInfo.TokenEndpoint,
					JWKSURL:       asInfo.JWKSURL,
					ClientID:      asInfo.ClientID,
					ClientSecret:  asInfo.ClientSecret,
				})
			})
		}),
		server.WithHandlerWrap(cors),
	); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

// --- In-process oneauth AS ---

type inProcessAS struct {
	ASURL         string
	TokenEndpoint string
	JWKSURL       string
	ClientID      string
	ClientSecret  string
	cleanup       func()
}

// startInProcessAS spins up an in-process oneauth Authorization Server with
// RS256 signing, RAR support, and a pre-registered confidential client.
func startInProcessAS() (*inProcessAS, error) {
	// Generate AS RSA keypair.
	privPEM, pubPEM, err := utils.GenerateRSAKeyPair(2048)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	parsed, err := utils.ParsePrivateKeyPEM(privPEM)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	privKey := parsed.(*rsa.PrivateKey)

	// Keystore: register the AS's *public* key (PEM bytes) under a sentinel
	// client_id so the JWKSHandler publishes it. APIAuth signs with the
	// matching private key (passed via JWTSigningKey, below).
	ks := keys.NewInMemoryKeyStore()
	if err := ks.RegisterKey(asKeyID, pubPEM, "RS256"); err != nil {
		return nil, fmt.Errorf("register AS key: %w", err)
	}

	// AS components.
	registrar := admin.NewAppRegistrar(ks, admin.NewNoAuth())
	apiAuth := &apiauth.APIAuth{
		JWTSigningAlg:  "RS256",
		JWTSigningKey:  privKey,
		JWTVerifyKey:   &privKey.PublicKey,
		ClientKeyStore: ks,
	}
	jwksHandler := &keys.JWKSHandler{KeyStore: ks}

	// metadataMu guards lazy initialization of asMetadataHandler with the AS
	// URL, which we don't know until httptest.NewServer is created.
	var asMetadataHandler http.Handler

	mux := http.NewServeMux()
	mux.Handle("/apps/", registrar.Handler())
	mux.HandleFunc("POST /token", apiAuth.ServeHTTP)
	mux.Handle("GET /.well-known/jwks.json", jwksHandler)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if asMetadataHandler == nil {
			http.Error(w, "AS not yet initialized", http.StatusServiceUnavailable)
			return
		}
		asMetadataHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(mux)
	apiAuth.JWTIssuer = ts.URL

	asMetadataHandler = apiauth.NewASMetadataHandler(&apiauth.ASServerMetadata{
		Issuer:                             ts.URL,
		TokenEndpoint:                      ts.URL + "/token",
		JWKSURI:                            ts.URL + "/.well-known/jwks.json",
		GrantTypesSupported:                []string{"client_credentials"},
		TokenEndpointAuthMethods:           []string{"client_secret_post"},
		AuthorizationDetailsTypesSupported: []string{"payment_initiation"},
	})

	// Register a confidential client for the demo.
	body, _ := json.Marshal(map[string]any{
		"client_name":                "fine-grained-auth-demo",
		"grant_types":                []string{"client_credentials"},
		"token_endpoint_auth_method": "client_secret_post",
		"scope":                      strings.Join([]string{scopeRead, scopeCall}, " "),
	})
	resp, err := http.Post(ts.URL+"/apps/dcr", "application/json", bytes.NewReader(body))
	if err != nil {
		ts.Close()
		return nil, fmt.Errorf("DCR: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		ts.Close()
		return nil, fmt.Errorf("DCR returned %d: %s", resp.StatusCode, respBody)
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		ts.Close()
		return nil, fmt.Errorf("DCR decode: %w", err)
	}

	return &inProcessAS{
		ASURL:         ts.URL,
		TokenEndpoint: ts.URL + "/token",
		JWKSURL:       ts.URL + "/.well-known/jwks.json",
		ClientID:      reg.ClientID,
		ClientSecret:  reg.ClientSecret,
		cleanup:       ts.Close,
	}, nil
}

// --- Document store ---

// docDir is where read_document and update_document persist content. It's
// allocated per-server-instance via os.MkdirTemp() so concurrent demos don't
// clobber each other. Path is logged on startup so users can inspect with
// `cat $docDir/doc-123.txt`. Cleaned up on graceful shutdown.
var docDir string

// seedDocs creates a fresh temp dir for the document store and writes a few
// sample documents the demo can read.
func seedDocs() error {
	dir, err := os.MkdirTemp("", "fine-grained-auth-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	docDir = dir
	samples := map[string]string{
		"doc-001": "Project kickoff notes — kickoff is on Monday at 10am. Bring the roadmap doc.",
		"doc-123": "Q2 strategy memo — focus on three pillars: reliability, observability, and developer experience.",
		"doc-456": "Architecture review — the new auth middleware was approved. Rollout planned for week 14.",
	}
	for id, content := range samples {
		if err := os.WriteFile(docPath(id), []byte(content), 0o644); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
	}
	return nil
}

// docPath returns the on-disk path for a given docID. Sanitizes to prevent
// path traversal — only the basename is used.
func docPath(docID string) string {
	return filepath.Join(docDir, filepath.Base(docID)+".txt")
}

// --- Tool registration ---

func registerTools(srv *server.Server) {
	srv.RegisterTool(
		core.ToolDef{
			Name:           "read_document",
			Description:    "Read a document from the document store. Requires tools-read scope.",
			RequiredScopes: []string{scopeRead},
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"docId": {"type": "string", "description": "Document ID (seeds: doc-001, doc-123, doc-456)"}
				}
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				DocID string `json:"docId"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.DocID == "" {
				args.DocID = "doc-001"
			}
			body, err := os.ReadFile(docPath(args.DocID))
			if err != nil {
				if os.IsNotExist(err) {
					return core.ErrorResult(fmt.Sprintf(
						"document %q not found in %s", args.DocID, docDir)), nil
				}
				return core.ErrorResult(fmt.Sprintf("read %q: %v", args.DocID, err)), nil
			}
			return core.TextResult(fmt.Sprintf("[%s] %s", args.DocID, body)), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:           "update_document",
			Description:    "Write content to a document in the store. Requires tools-call scope.",
			RequiredScopes: []string{scopeCall},
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"docId":   {"type": "string", "description": "Document ID (will be created if it doesn't exist)"},
					"content": {"type": "string", "description": "New content"}
				},
				"required": ["docId", "content"]
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				DocID   string `json:"docId"`
				Content string `json:"content"`
			}
			json.Unmarshal(req.Arguments, &args)
			path := docPath(args.DocID)
			if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
				return core.ErrorResult(fmt.Sprintf("write %q: %v", args.DocID, err)), nil
			}
			return core.TextResult(fmt.Sprintf(
				"Document %q updated (%d bytes written to %s).",
				args.DocID, len(args.Content), path)), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "initiate_payment",
			Description: "Initiate a payment. Requires an RAR-bound token with payment_initiation authorization_details (UC3).",
			// Authorization is checked by paymentAuthorizationMiddleware (registered
			// alongside scope middleware in serve()). Handler runs only on success.
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
			return core.TextResult(fmt.Sprintf(
				"Payment of %s %s to %s initiated successfully.",
				args.Amount, args.Currency, args.Payee)), nil
		},
	)
}

// paymentAuthorizationMiddleware short-circuits tools/call for initiate_payment
// when the bearer token's authorization_details claim doesn't include a
// matching payment_initiation entry. Returns a JSON-RPC error response with
// the SEP-2643 denial envelope (credentialDisposition: additional + RAR hint).
//
// Note: SEP-2643 specifies a JSON-RPC error code (Open Question §1, currently
// TBD). This example uses ErrCodeServerError as a placeholder; tracked in #317.
func paymentAuthorizationMiddleware() server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}
		var envelope struct {
			Name      string `json:"name"`
			Arguments struct {
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
				Payee    string `json:"payee"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req)
		}
		if envelope.Name != "initiate_payment" {
			return next(ctx, req)
		}

		args := envelope.Arguments
		if claimsAuthorizePayment(ctx, args.Amount, args.Currency, args.Payee) {
			return next(ctx, req)
		}

		// Build the SEP-2643 denial envelope.
		requested := paymentDetail(args.Amount, args.Currency, args.Payee)
		denial := core.AuthorizationDenial{
			Reason:                "insufficient_authorization",
			CredentialDisposition: "additional",
			RemediationHints: []core.RemediationHint{
				core.OAuthAuthorizationDetailsHint([]oacore.AuthorizationDetail{requested}),
			},
		}
		return core.NewErrorResponseWithData(req.ID,
			core.ErrCodeServerError,
			fmt.Sprintf("Payment of %s %s to %s requires a transaction-specific credential.",
				args.Amount, args.Currency, args.Payee),
			map[string]any{
				"authorization": denial,
			},
		), nil
	}
}

// paymentDetail constructs the RFC 9396 authorization_details entry for a
// payment_initiation operation.
func paymentDetail(amount, currency, payee string) oacore.AuthorizationDetail {
	return oacore.AuthorizationDetail{
		Type:    "payment_initiation",
		Actions: []string{"initiate"},
		Extra: map[string]any{
			"instructedAmount": map[string]any{
				"currency": currency,
				"amount":   amount,
			},
			"creditorName": payee,
		},
	}
}

// claimsAuthorizePayment checks whether the request context's auth claims
// carry an authorization_details entry of type payment_initiation that matches
// the requested amount/currency/payee.
func claimsAuthorizePayment(ctx context.Context, amount, currency, payee string) bool {
	claims := core.AuthClaims(ctx)
	if claims == nil || claims.Extra == nil {
		return false
	}
	raw, ok := claims.Extra["authorization_details"]
	if !ok {
		return false
	}
	// Re-marshal then unmarshal into a typed slice.
	rawJSON, _ := json.Marshal(raw)
	var details []oacore.AuthorizationDetail
	if err := json.Unmarshal(rawJSON, &details); err != nil {
		return false
	}
	for _, d := range details {
		if d.Type != "payment_initiation" {
			continue
		}
		amt, _ := nestedString(d.Extra, "instructedAmount", "amount")
		cur, _ := nestedString(d.Extra, "instructedAmount", "currency")
		creditor, _ := d.Extra["creditorName"].(string)
		if amt == amount && cur == currency && creditor == payee {
			return true
		}
	}
	return false
}

func nestedString(m map[string]any, path ...string) (string, bool) {
	cur := any(m)
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = mm[p]
	}
	s, ok := cur.(string)
	return s, ok
}


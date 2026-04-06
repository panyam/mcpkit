// Command testclient is the MCP auth conformance test client.
// It is invoked by the MCP conformance runner via:
//
//	npx @modelcontextprotocol/conformance client --command "go run ./cmd/testclient" --suite auth
//
// The conformance runner starts a mock MCP server + mock OAuth AS, then invokes
// this binary with the server URL appended as an argument. Environment variables:
//
//	MCP_CONFORMANCE_SCENARIO — scenario name (e.g., "auth/metadata-default")
//	MCP_CONFORMANCE_CONTEXT  — JSON with scenario-specific data (e.g., pre-registered credentials)
//
// This client performs the full MCP OAuth flow headlessly (no browser — follows
// redirects via HTTP) and then sends an initialize request.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: testclient <server-url>")
	}
	serverURL := os.Args[1]
	scenario := os.Getenv("MCP_CONFORMANCE_SCENARIO")
	contextJSON := os.Getenv("MCP_CONFORMANCE_CONTEXT")

	log.Printf("scenario=%s server=%s", scenario, serverURL)

	var ctx conformanceContext
	if contextJSON != "" {
		json.Unmarshal([]byte(contextJSON), &ctx)
	}

	// Use a client that does NOT follow redirects (we handle them manually)
	httpClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: Probe server — expect 401 with WWW-Authenticate
	// Per MCP spec (2025-11-25): clients MUST include Accept header with both types.
	// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#sending-messages-to-the-server
	log.Println("Step 1: Probing server for 401...")
	probeReq, _ := http.NewRequest("POST", serverURL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcpkit-testclient","version":"0.1.0"}}}`))
	probeReq.Header.Set("Content-Type", "application/json")
	probeReq.Header.Set("Accept", "application/json, text/event-stream")

	probeResp, err := httpClient.Do(probeReq)
	if err != nil {
		log.Fatalf("probe: %v", err)
	}
	io.Copy(io.Discard, probeResp.Body)
	probeResp.Body.Close()

	if probeResp.StatusCode != 401 {
		log.Printf("probe returned %d (not 401) — server may not require auth, trying direct initialize", probeResp.StatusCode)
		// Try direct initialize without auth
		doInitialize(httpClient, serverURL, "")
		return
	}

	// Step 2: Parse WWW-Authenticate header
	wwwAuth := probeResp.Header.Get("WWW-Authenticate")
	log.Printf("Step 2: WWW-Authenticate: %s", wwwAuth)

	resourceMetadataURL := extractParam(wwwAuth, "resource_metadata")
	scopeStr := extractParam(wwwAuth, "scope")

	// Step 3: Fetch Protected Resource Metadata
	log.Println("Step 3: Fetching PRM...")
	var prmURL string
	if resourceMetadataURL != "" {
		prmURL = resourceMetadataURL
	} else {
		// Fallback: try path-based well-known URI
		u, _ := url.Parse(serverURL)
		prmURL = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
	}

	prmResp, err := httpClient.Get(prmURL)
	if err != nil {
		log.Fatalf("PRM fetch: %v", err)
	}
	prmBody, _ := io.ReadAll(prmResp.Body)
	prmResp.Body.Close()

	if prmResp.StatusCode == 404 && resourceMetadataURL == "" {
		// Fallback to root well-known
		u, _ := url.Parse(serverURL)
		rootPRM := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
		log.Printf("Path-based PRM 404, trying root: %s", rootPRM)
		prmResp2, err := httpClient.Get(rootPRM)
		if err != nil {
			log.Fatalf("root PRM fetch: %v", err)
		}
		prmBody, _ = io.ReadAll(prmResp2.Body)
		prmResp2.Body.Close()
	}

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(prmBody, &prm); err != nil {
		log.Fatalf("PRM parse: %v (body: %s)", err, prmBody)
	}
	log.Printf("PRM: resource=%s, AS=%v", prm.Resource, prm.AuthorizationServers)

	if len(prm.AuthorizationServers) == 0 {
		log.Fatal("PRM has no authorization_servers")
	}

	// Step 4: Discover Authorization Server Metadata
	issuer := prm.AuthorizationServers[0]
	log.Printf("Step 4: Discovering AS metadata for %s...", issuer)

	asMeta := discoverAS(httpClient, issuer)
	log.Printf("AS: auth=%s token=%s registration=%s",
		asMeta.AuthorizationEndpoint, asMeta.TokenEndpoint, asMeta.RegistrationEndpoint)

	// Step 5: Client Registration
	clientID := ctx.ClientID
	clientSecret := ctx.ClientSecret

	if clientID == "" {
		// Try Dynamic Client Registration if available
		if asMeta.RegistrationEndpoint != "" {
			log.Println("Step 5: Dynamic Client Registration...")
			clientID, clientSecret = registerClient(httpClient, asMeta.RegistrationEndpoint)
		} else {
			log.Fatal("no client_id and no registration endpoint")
		}
	} else {
		log.Printf("Step 5: Using pre-registered client_id=%s", clientID)
	}

	// Step 6: PKCE + Authorization Request
	log.Println("Step 6: PKCE authorization...")
	verifier := generateCodeVerifier()
	challenge := computeS256Challenge(verifier)
	stateBytes := make([]byte, 16)
	rand.Read(stateBytes)
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	// Determine scopes
	var scopes string
	if scopeStr != "" {
		scopes = scopeStr
	} else if len(prm.ScopesSupported) > 0 {
		scopes = strings.Join(prm.ScopesSupported, " ")
	}

	// Build authorization URL
	authParams := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {"http://127.0.0.1:0/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if scopes != "" {
		authParams.Set("scope", scopes)
	}
	if prm.Resource != "" {
		authParams.Set("resource", prm.Resource)
	}

	authURL := asMeta.AuthorizationEndpoint + "?" + authParams.Encode()
	log.Printf("Auth URL: %s", authURL)

	// Follow the authorization flow (conformance mock AS auto-approves)
	authResp, err := httpClient.Get(authURL)
	if err != nil {
		log.Fatalf("auth request: %v", err)
	}

	// The mock AS should redirect with code
	var code string
	if authResp.StatusCode == 302 || authResp.StatusCode == 303 {
		io.Copy(io.Discard, authResp.Body)
		authResp.Body.Close()
		location := authResp.Header.Get("Location")
		log.Printf("Auth redirect: %s", location)
		redirectURL, _ := url.Parse(location)
		code = redirectURL.Query().Get("code")
		returnedState := redirectURL.Query().Get("state")
		if returnedState != state {
			log.Printf("WARNING: state mismatch: got %s, want %s", returnedState, state)
		}
	} else {
		body, _ := io.ReadAll(authResp.Body)
		authResp.Body.Close()
		log.Printf("Auth response %d: %s", authResp.StatusCode, body)
		log.Fatal("expected redirect from authorization endpoint")
	}

	if code == "" {
		log.Fatal("no authorization code received")
	}
	log.Printf("Got auth code: %s", code[:min(len(code), 10)]+"...")

	// Step 7: Token Exchange
	log.Println("Step 7: Token exchange...")
	tokenParams := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {"http://127.0.0.1:0/callback"},
		"client_id":     {clientID},
	}
	if prm.Resource != "" {
		tokenParams.Set("resource", prm.Resource)
	}

	// Apply client authentication based on AS-supported methods.
	// Per OAuth 2.1: client MUST use the token_endpoint_auth_method from its registration
	// or one supported by the AS.
	// https://datatracker.ietf.org/doc/html/draft-ietf-oauth-v2-1-13#section-3.2.1
	authMethod := applyTokenEndpointAuth(tokenParams, clientID, clientSecret, asMeta.TokenAuthMethods)

	tokenReq, _ := http.NewRequest("POST", asMeta.TokenEndpoint, strings.NewReader(tokenParams.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// If using client_secret_basic, set the Basic auth header
	if authMethod == "client_secret_basic" {
		tokenReq.SetBasicAuth(clientID, clientSecret)
	}

	tokenResp, err := httpClient.Do(tokenReq)
	if err != nil {
		log.Fatalf("token request: %v", err)
	}
	tokenBody, _ := io.ReadAll(tokenResp.Body)
	tokenResp.Body.Close()

	var tokenResult struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		log.Fatalf("token parse: %v (body: %s)", err, tokenBody)
	}
	if tokenResult.AccessToken == "" {
		log.Fatalf("no access_token in response: %s", tokenBody)
	}
	log.Printf("Got access token (type=%s, expires_in=%d)", tokenResult.TokenType, tokenResult.ExpiresIn)

	// Step 8: Initialize MCP with token
	log.Println("Step 8: MCP initialize with token...")
	doInitialize(httpClient, serverURL, tokenResult.AccessToken)

	log.Println("SUCCESS: auth flow complete")
}

// doInitialize sends the MCP initialize + initialized handshake.
func doInitialize(httpClient *http.Client, serverURL, token string) {
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcpkit-testclient","version":"0.1.0"}}}`

	req, _ := http.NewRequest("POST", serverURL, strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Fatalf("initialize: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Fatalf("initialize returned %d: %s", resp.StatusCode, body)
	}

	log.Printf("Initialize response: %s", body)

	// Send notifications/initialized
	notifBody := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req2, _ := http.NewRequest("POST", serverURL, strings.NewReader(notifBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req2.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		req2.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp2, err := httpClient.Do(req2)
	if err != nil {
		log.Fatalf("initialized notification: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
}

// discoverAS tries OAuth AS metadata endpoints with RFC 8414 + OIDC fallback.
func discoverAS(httpClient *http.Client, issuer string) asMetadata {
	u, _ := url.Parse(issuer)
	path := strings.TrimRight(u.Path, "/")

	var urls []string
	if path != "" {
		// Path-based issuer: try path insertion first
		urls = []string{
			u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server" + path,
			u.Scheme + "://" + u.Host + "/.well-known/openid-configuration" + path,
			issuer + "/.well-known/openid-configuration",
		}
	} else {
		urls = []string{
			issuer + "/.well-known/oauth-authorization-server",
			issuer + "/.well-known/openid-configuration",
		}
	}

	for _, metaURL := range urls {
		log.Printf("  Trying AS metadata: %s", metaURL)
		resp, err := httpClient.Get(metaURL)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			var meta asMetadata
			if json.Unmarshal(body, &meta) == nil && meta.TokenEndpoint != "" {
				return meta
			}
		}
	}

	log.Fatal("could not discover AS metadata from any well-known endpoint")
	return asMetadata{}
}

// registerClient performs RFC 7591 Dynamic Client Registration.
func registerClient(httpClient *http.Client, endpoint string) (clientID, clientSecret string) {
	regBody, _ := json.Marshal(map[string]any{
		"client_name":                "mcpkit-testclient",
		"redirect_uris":             []string{"http://127.0.0.1:0/callback"},
		"grant_types":               []string{"authorization_code"},
		"response_types":            []string{"code"},
		"token_endpoint_auth_method": "none",
	})

	resp, err := httpClient.Post(endpoint, "application/json", bytes.NewReader(regBody))
	if err != nil {
		log.Fatalf("DCR: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var result struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Fatalf("DCR parse: %v (body: %s)", err, body)
	}
	if result.ClientID == "" {
		log.Fatalf("DCR returned no client_id: %s", body)
	}
	log.Printf("Registered client_id=%s", result.ClientID)
	return result.ClientID, result.ClientSecret
}

// --- PKCE helpers ---

func generateCodeVerifier() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func computeS256Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// --- WWW-Authenticate parsing ---

func extractParam(header, name string) string {
	search := name + "="
	idx := strings.Index(header, search)
	if idx < 0 {
		return ""
	}
	rest := header[idx+len(search):]
	if len(rest) > 0 && rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return rest[1:]
		}
		return rest[1 : end+1]
	}
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// --- Token endpoint auth method selection ---

// selectAuthMethod determines which token endpoint authentication method to use
// based on the AS's supported methods and whether we have a client secret.
// Per OAuth 2.1: client MUST use a method supported by the AS.
// https://datatracker.ietf.org/doc/html/draft-ietf-oauth-v2-1-13#section-3.2.1
func selectAuthMethod(supported []string, clientSecret string) string {
	if clientSecret == "" {
		return "none"
	}
	// Prefer client_secret_post if supported (most interoperable)
	for _, m := range supported {
		if m == "client_secret_post" {
			return "client_secret_post"
		}
	}
	for _, m := range supported {
		if m == "client_secret_basic" {
			return "client_secret_basic"
		}
	}
	// Default: basic (most common)
	return "client_secret_basic"
}

// applyTokenEndpointAuth adds client credentials to the token request params
// when using client_secret_post. For client_secret_basic, credentials go in
// the Authorization header (handled by the caller using the returned method).
// Returns the selected auth method so the caller can set Basic auth if needed.
func applyTokenEndpointAuth(params url.Values, clientID, clientSecret string, supportedMethods []string) string {
	method := selectAuthMethod(supportedMethods, clientSecret)
	log.Printf("Token endpoint auth method: %s", method)

	switch method {
	case "client_secret_post":
		params.Set("client_id", clientID)
		params.Set("client_secret", clientSecret)
	case "client_secret_basic":
		// Basic auth is set on the request header, not in params.
		// client_id is already in params from the caller.
	case "none":
		// Public client — client_id already in params.
	}
	return method
}

// --- Types ---

type conformanceContext struct {
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type asMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

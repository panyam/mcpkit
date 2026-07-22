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
// This client uses OAuthTokenSource from ext/auth with oneauth's FollowRedirects
// for headless OAuth (no browser — HTTP redirect following).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	oneauthclient "github.com/panyam/oneauth/client"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: testclient <server-url>")
	}
	serverURL := os.Args[1]
	scenario := os.Getenv("MCP_CONFORMANCE_SCENARIO")
	contextJSON := os.Getenv("MCP_CONFORMANCE_CONTEXT")

	log.Printf("scenario=%s server=%s", scenario, serverURL)

	// SEP-2322 client scenarios — the upstream fixtures are bare-HTTP
	// JSON-RPC endpoints (no initialize handshake, no server/discover),
	// so mcpkit's connected client doesn't apply. Drive them via raw
	// HTTP through driveSEP2322ClientRequestState, which exercises the
	// MRTR retry contract the scenario grades:
	//   - echo requestState byte-for-byte on retry,
	//   - omit requestState when the server didn't send one,
	//   - use a different JSON-RPC id on retry,
	//   - keep MRTR state isolated across unrelated tool calls.
	if scenario == "sep-2322-client-request-state" {
		if err := driveSEP2322ClientRequestState(serverURL); err != nil {
			log.Fatalf("sep-2322-client-request-state: %v", err)
		}
		log.Println("SUCCESS: sep-2322-client-request-state driven")
		return
	}

	// SEP-2575 request-metadata scenario — the upstream fixture is a bare
	// HTTP server (not an MCP server: no initialize, but it DOES handle
	// server/discover so a stateless-wire client connects cleanly). Drive
	// it through driveRequestMetadata, which exercises the per-request
	// _meta envelope + MCP-Protocol-Version header checks the scenario
	// grades.
	if scenario == "request-metadata" {
		if err := driveRequestMetadata(serverURL); err != nil {
			log.Fatalf("request-metadata: %v", err)
		}
		log.Println("SUCCESS: request-metadata driven")
		return
	}

	var ctx conformanceContext
	if contextJSON != "" {
		json.Unmarshal([]byte(contextJSON), &ctx)
	}

	// Step 1: Try connecting without auth first.
	// Some conformance scenarios don't require auth, or the server accepts
	// initialize but returns 403 on subsequent requests (scope step-up).
	log.Println("Step 1: Trying direct connect (no auth)...")
	noAuthClient := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithClientLogging(log.Default()),
		client.WithElicitationHandler(conformanceElicitationHandler),
		// Open a background GET SSE so server-to-client requests
		// (elicitation/create, sampling/createMessage) sent on the
		// session-wide stream by SDK-conformant servers reach us.
		// Without this, scenarios that drive elicitation via
		// StreamableHTTPServerTransport hang waiting on the wrong stream.
		client.WithGetSSEStream())

	if err := noAuthClient.Connect(); err == nil {
		// Connected without auth — verify session works
		tools, err := noAuthClient.ListTools(context.Background())
		if err == nil {
			switch {
			case len(ctx.ToolCalls) > 0:
				// Directed: scenario specified exact toolCalls to invoke.
				driveToolCalls(noAuthClient, ctx.ToolCalls)
			case len(tools) > 0:
				// Fallback: best-effort call the first tool with
				// schema-synthesized args. Mirrors the OAuth-success path
				// so scenarios that don't direct toolCalls still exercise
				// a tools/call request against the server (required by
				// e.g. SEP-2243 http-invalid-tool-headers, which grades
				// whether the client calls the valid_tool the server
				// advertises, and tools_call, which grades the argument
				// types).
				toolName := tools[0].Name
				log.Printf("Calling tool %q (no-auth fallback)...", toolName)
				if _, err := noAuthClient.ToolCall(toolName, synthArgs(tools[0].InputSchema)); err != nil {
					log.Printf("tools/call %q: %v (may be expected)", toolName, err)
				}
			}
			driveStandardHeadersCoverage(noAuthClient)
			noAuthClient.Close()
			log.Println("SUCCESS: connected without auth")
			return
		}

		// Check if the error indicates auth is required
		noAuthClient.Close()
		var authErr *client.ClientAuthError
		if !errors.As(err, &authErr) || (authErr.StatusCode != 401 && authErr.StatusCode != 403) {
			log.Printf("tools/list error (non-auth): %v (non-fatal)", err)
			log.Println("SUCCESS: connected without auth")
			return
		}
		log.Printf("Server requires auth (%d) — proceeding with OAuth flow", authErr.StatusCode)
	} else {
		log.Printf("Direct connect failed: %v — proceeding with OAuth flow", err)
	}

	// Step 2: Build a TokenSource. Pick the variant by AS grant_types_supported:
	// SEP-1046 scenarios advertise only client_credentials (no authorization_code),
	// so a quick discovery probe tells us which grant the AS will accept. The
	// presence of PrivateKeyPEM additionally pins us to the private_key_jwt
	// variant — no other scenario provides one.
	ts := pickTokenSource(serverURL, ctx)

	// Step 3: Connect with auth token.
	log.Println("Step 3: MCP initialize with OAuth token...")
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithTokenSource(ts),
		client.WithClientLogging(log.Default()),
		client.WithElicitationHandler(conformanceElicitationHandler),
		client.WithGetSSEStream(),
	)

	if err := c.Connect(); err != nil {
		log.Fatalf("MCP connect: %v", err)
	}
	defer c.Close()

	// Step 4: Verify session — tools/list + tool call(s).
	// The client transport handles 401 (token refresh) and 403 (scope step-up
	// via OAuthTokenSource.TokenForScopes) automatically.
	log.Println("Step 4: Verifying session with tools/list...")
	tools, err := c.ListTools(context.Background())
	if err != nil {
		log.Printf("tools/list: %v (non-fatal)", err)
	} else {
		log.Printf("tools/list: %d tools available", len(tools))

		switch {
		case len(ctx.ToolCalls) > 0:
			// Directed: scenario specified exact toolCalls to invoke.
			driveToolCalls(c, ctx.ToolCalls)
		case len(tools) > 0:
			// Fallback: best-effort call the first tool with
			// schema-synthesized args.
			toolName := tools[0].Name
			log.Printf("Calling tool %q (default fallback)...", toolName)
			if _, err := c.ToolCall(toolName, synthArgs(tools[0].InputSchema)); err != nil {
				log.Printf("tools/call %q: %v (may be expected)", toolName, err)
			} else {
				log.Printf("tools/call %q: ok", toolName)
			}
		}
		driveStandardHeadersCoverage(c)
	}

	log.Println("SUCCESS: auth flow complete")
}

// driveStandardHeadersCoverage exercises resources/* and prompts/* on a
// connected client so the SEP-2243 http-standard-headers scenario's
// `ClientMcp{Method,Name}Header_{resources,prompts}_*` checks observe the
// routing headers on the wire (otherwise they stay SKIPPED). Errors are
// non-fatal — most non-SEP-2243 scenario mocks return generic responses
// to these methods and the test goal is header emission, not response
// fidelity. Safe to call against any connected client; it's a few extra
// POSTs per scenario run, which is cheap relative to the audit value.
func driveStandardHeadersCoverage(c *client.Client) {
	if _, err := c.Call("resources/list", map[string]any{}); err != nil {
		log.Printf("resources/list: %v (non-fatal — coverage)", err)
	}
	if _, err := c.Call("resources/read", map[string]any{"uri": "test://coverage"}); err != nil {
		log.Printf("resources/read: %v (non-fatal — coverage)", err)
	}
	if _, err := c.Call("prompts/list", map[string]any{}); err != nil {
		log.Printf("prompts/list: %v (non-fatal — coverage)", err)
	}
	if _, err := c.Call("prompts/get", map[string]any{"name": "coverage"}); err != nil {
		log.Printf("prompts/get: %v (non-fatal — coverage)", err)
	}
}

// conformanceElicitationHandler returns an accept-with-empty-content
// response for any elicitation/create. mcpkit's client library then
// fills in SEP-1034 schema defaults before forwarding to the server —
// which is exactly what the SEP-1034 conformance scenario grades. A
// real interactive user would prompt; this fixture short-circuits to
// the spec's "user accepted with no overrides" path so the default-
// filling behavior is observable on the wire.
func conformanceElicitationHandler(_ context.Context, _ core.ElicitationRequest) (core.ElicitationResult, error) {
	return core.ElicitationResult{Action: "accept", Content: nil}, nil
}

// conformanceContext holds scenario-specific data from the conformance runner.
type conformanceContext struct {
	Name             string         `json:"name"`
	ClientID         string         `json:"client_id"`
	ClientSecret     string         `json:"client_secret"`
	PrivateKeyPEM    string         `json:"private_key_pem"`
	SigningAlgorithm string         `json:"signing_algorithm"`
	IdpClientID      string         `json:"idp_client_id"`
	IdpIDToken       string         `json:"idp_id_token"`
	IdpIssuer        string         `json:"idp_issuer"`
	IdpTokenEndpoint string         `json:"idp_token_endpoint"`
	ValidJwt         string         `json:"valid_jwt"`
	ToolCalls        []toolCallSpec `json:"toolCalls"`
}

// toolCallSpec is one directed tools/call invocation requested by the scenario.
// Upstream's SEP-2243 http-custom-headers scenario sends a toolCalls array
// instead of relying on the driver's "call first tool" default; each entry
// specifies the exact tool name and arguments to invoke.
type toolCallSpec struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// pickTokenSource decides which TokenSource to use based on context shape:
// SEP-990 (idp_id_token), SEP-1046 private_key_jwt (private_key_pem),
// SEP-1046 basic (AS advertises only client_credentials), or the default
// authorization_code flow. The AS-discovery probe is gated on ClientID != ""
// to keep DCR scenarios with no pre-registered credentials on the
// OAuthTokenSource fast path without spurious PRM/AS round trips.
func pickTokenSource(serverURL string, ctx conformanceContext) core.TokenSource {
	if ctx.ValidJwt != "" {
		log.Println("Step 2: ctx has valid_jwt → JWTBearerTokenSource (RFC 7523 WIF)")
		return &auth.JWTBearerTokenSource{
			ServerURL:     serverURL,
			ClientID:      ctx.ClientID,
			Assertion:     ctx.ValidJwt,
			AllowInsecure: true,
		}
	}
	if ctx.IdpIDToken != "" && ctx.IdpTokenEndpoint != "" {
		log.Println("Step 2: ctx has idp_id_token + idp_token_endpoint → EnterpriseManagedTokenSource (SEP-990)")
		return &auth.EnterpriseManagedTokenSource{
			ServerURL:        serverURL,
			ClientID:         ctx.ClientID,
			ClientSecret:     ctx.ClientSecret,
			IdpClientID:      ctx.IdpClientID,
			IdpIDToken:       ctx.IdpIDToken,
			IdpTokenEndpoint: ctx.IdpTokenEndpoint,
			AllowInsecure:    true,
		}
	}
	if ctx.PrivateKeyPEM != "" {
		log.Println("Step 2: ctx has private_key_pem → ClientCredentialsTokenSource (private_key_jwt)")
		return &auth.ClientCredentialsTokenSource{
			ServerURL:        serverURL,
			ClientID:         ctx.ClientID,
			PrivateKeyPEM:    ctx.PrivateKeyPEM,
			SigningAlgorithm: ctx.SigningAlgorithm,
			AllowInsecure:    true,
		}
	}
	if ctx.ClientID != "" {
		info, err := auth.DiscoverMCPAuth(serverURL)
		if err == nil && info.ASMetadata != nil {
			grants := info.ASMetadata.GrantTypesSupported
			if slices.Contains(grants, "client_credentials") && !slices.Contains(grants, "authorization_code") {
				log.Println("Step 2: AS advertises only client_credentials → ClientCredentialsTokenSource (basic)")
				return &auth.ClientCredentialsTokenSource{
					ServerURL:     serverURL,
					ClientID:      ctx.ClientID,
					ClientSecret:  ctx.ClientSecret,
					AllowInsecure: true,
				}
			}
		}
	}
	log.Println("Step 2: Setting up OAuthTokenSource...")
	return &auth.OAuthTokenSource{
		ServerURL:     serverURL,
		ClientID:      ctx.ClientID,
		ClientSecret:  ctx.ClientSecret,
		EnableDCR:     true,
		AllowInsecure: true,
		OpenBrowser:   oneauthclient.FollowRedirects(nil),
	}
}

// driveToolCalls invokes each scenario-directed tool call in sequence via the
// canonical client.Client.ToolCall typed helper. Errors are logged but
// non-fatal: some scenarios deliberately set up calls that should fail
// (custom-header validation, etc.) and grade the wire-level behavior of the
// call itself rather than its success.
func driveToolCalls(c *client.Client, calls []toolCallSpec) {
	for i, tc := range calls {
		log.Printf("Directed call %d/%d: tool=%q with %d args", i+1, len(calls), tc.Name, len(tc.Arguments))
		if _, err := c.ToolCall(tc.Name, tc.Arguments); err != nil {
			log.Printf("tools/call %q: %v (non-fatal — scenario may expect)", tc.Name, err)
		}
	}
}

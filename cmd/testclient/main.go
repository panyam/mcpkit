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
		tools, err := noAuthClient.ListTools()
		if err == nil {
			switch {
			case len(ctx.ToolCalls) > 0:
				// Directed: scenario specified exact toolCalls to invoke.
				driveToolCalls(noAuthClient, ctx.ToolCalls)
			case len(tools) > 0:
				// Fallback: best-effort call the first tool, no args.
				// Mirrors the OAuth-success path so scenarios that don't
				// direct toolCalls still exercise a tools/call request
				// against the server (required by e.g. SEP-2243
				// http-invalid-tool-headers, which grades whether the
				// client calls the valid_tool the server advertises).
				toolName := tools[0].Name
				log.Printf("Calling tool %q (no-auth fallback)...", toolName)
				if _, err := noAuthClient.ToolCall(toolName, map[string]any{}); err != nil {
					log.Printf("tools/call %q: %v (may be expected)", toolName, err)
				}
			}
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

	// Step 2: Build OAuthTokenSource with discovered credentials.
	// OAuthTokenSource handles: PRM discovery → AS metadata → PKCE → DCR → token exchange.
	// FollowRedirects provides headless OAuth (follows 302s instead of opening browser).
	log.Println("Step 2: Setting up OAuthTokenSource...")
	ts := &auth.OAuthTokenSource{
		ServerURL:     serverURL,
		ClientID:      ctx.ClientID,
		ClientSecret:  ctx.ClientSecret,
		EnableDCR:     true,  // fallback to DCR if no pre-registered client_id
		AllowInsecure: true,  // conformance mock AS uses HTTP, not HTTPS
		OpenBrowser:   oneauthclient.FollowRedirects(nil), // nil = default client (follows redirects)
	}

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
	tools, err := c.ListTools()
	if err != nil {
		log.Printf("tools/list: %v (non-fatal)", err)
	} else {
		log.Printf("tools/list: %d tools available", len(tools))

		switch {
		case len(ctx.ToolCalls) > 0:
			// Directed: scenario specified exact toolCalls to invoke.
			driveToolCalls(c, ctx.ToolCalls)
		case len(tools) > 0:
			// Fallback: best-effort call the first tool, no args.
			toolName := tools[0].Name
			log.Printf("Calling tool %q (default fallback)...", toolName)
			if _, err := c.ToolCall(toolName, map[string]any{}); err != nil {
				log.Printf("tools/call %q: %v (may be expected)", toolName, err)
			} else {
				log.Printf("tools/call %q: ok", toolName)
			}
		}
	}

	log.Println("SUCCESS: auth flow complete")
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
	Name         string         `json:"name"`
	ClientID     string         `json:"client_id"`
	ClientSecret string         `json:"client_secret"`
	ToolCalls    []toolCallSpec `json:"toolCalls"`
}

// toolCallSpec is one directed tools/call invocation requested by the scenario.
// Upstream's SEP-2243 http-custom-headers scenario sends a toolCalls array
// instead of relying on the driver's "call first tool" default; each entry
// specifies the exact tool name and arguments to invoke.
type toolCallSpec struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
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

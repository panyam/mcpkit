// Auth example — one MCP server demonstrating all auth patterns layered together,
// plus a demokit-driven scripted MCP host walking through eight exercises.
//
// Two-process architecture (matches examples/fine-grained-auth/):
//
//	Terminal 1:  make serve         # MCP server + in-process AS on :8080
//	Terminal 2:  make run           # demokit walkthrough
//
// Patterns covered:
//   - Public discovery: tools/list works without a token
//   - JWT authentication: RS256 tokens validated via in-process AS's JWKS
//   - Scope enforcement: write-tool requires "write", admin-tool requires "admin"
//   - Session binding: switching tokens mid-session is rejected (anti-hijacking)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	authcommon "github.com/panyam/mcpkit/examples/auth/common"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
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

// --- Demo client ---

type bootstrapInfo struct {
	MCPURL       string `json:"mcp_url"`
	TokRead      string `json:"tok_read"`
	TokReadWrite string `json:"tok_read_write"`
	TokAll       string `json:"tok_all"`
	TokBob       string `json:"tok_bob"`

	// Deliberately-bad tokens for conformance testing — used by the
	// SEP-2356 file-inputs / MCP-auth conformance suites in
	// panyam/mcpconformance. Properly signed by this AS so the JWKS
	// signature check passes, but each violates one specific RFC 7519
	// claim that the server MUST reject. Demokit walkthroughs ignore
	// these fields.
	TokExpired       string `json:"tok_expired"`
	TokWrongAudience string `json:"tok_wrong_audience"`
	TokWrongIssuer   string `json:"tok_wrong_issuer"`

	// TokUpstreamAssertion is a JWT signed by a synthetic upstream IdP
	// the AS trusts (UpstreamIdpIssuer in common/setup.go). The
	// conformance suite uses this as a `subject_token` for the RFC 8693
	// token-exchange flow check (Phase 3c
	// auth-enterprise-managed-token-exchange-flow-shape) — POSTed to
	// /api/token with grant_type=token-exchange + subject_token_type=jwt
	// to verify the token-exchange response shape per RFC 8693 §2.2.
	TokUpstreamAssertion string `json:"tok_upstream_assertion"`
}

func runDemo() {
	serverURL := common.ServerURL()

	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("auth-unified-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("MCP Auth — Public Discovery + JWT + Scopes + Session Binding").
		Dir("auth").
		Description("Walks through auth patterns layered on a single mcpkit server: public method allowlist, JWT/JWKS validation, per-tool scope enforcement, and session hijacking prevention.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("AS", "Auth Server (in-process)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve        # MCP server + in-process AS on :8080",
		"Terminal 2:  make run          # this demo",
		"```",
	)

	demo.Section("Auth patterns covered",
		"1. **Public discovery** — `tools/list` works *without* a token (per spec, capability discovery should be permitted pre-auth).",
		"2. **JWT authentication** — protected methods require `Authorization: Bearer <RS256 JWT>`. The MCP server fetches the AS's JWKS and validates signatures.",
		"3. **Scope enforcement** — `write-tool` requires `write` scope; `admin-tool` requires `admin`. Missing scopes → HTTP 403 + `WWW-Authenticate: Bearer error=\"insufficient_scope\"`.",
		"4. **Session binding** — once a session is established with one user's token, requests on that session must come from the same subject. Swapping tokens mid-session is rejected to prevent session hijacking.",
	)

	var (
		boot            bootstrapInfo
		readClient      *client.Client
		readWriteClient *client.Client
	)

	// --- Step 1: Bootstrap ---
	demo.Step("Discover server URL + minted tokens").
		Arrow("Host", "Server", "GET /demo/bootstrap").
		DashedArrow("Server", "Host", "{mcp_url, tok_read, tok_read_write, tok_all, tok_bob}").
		Note("The server pre-mints four tokens for the demo and exposes them via a non-standard /demo/bootstrap endpoint. In production a host would do OAuth (or accept tokens via mcp.json config); this shortcut keeps the demo focused on auth behavior.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# The demo server pre-mints tokens and serves them at /demo/bootstrap.
# Capture the MCP URL and tokens into shell vars used by every step below.
BOOT=$(curl -s http://localhost:8080/demo/bootstrap)
MCP=$(echo "$BOOT" | jq -r .mcp_url)
TOK_READ=$(echo "$BOOT" | jq -r .tok_read)
TOK_RW=$(echo "$BOOT" | jq -r .tok_read_write)
TOK_BOB=$(echo "$BOOT" | jq -r .tok_bob)
echo "MCP=$MCP"`).Default(),
			demokit.MakeVariant("go", "go", `// Demo shortcut — production hosts would run OAuth instead.
resp, _ := http.Get(serverURL + "/demo/bootstrap")
defer resp.Body.Close()
var boot bootstrapInfo
json.NewDecoder(resp.Body).Decode(&boot)
// boot.MCPURL, boot.TokRead, boot.TokReadWrite, boot.TokAll, boot.TokBob`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			resp, err := http.Get(serverURL + "/demo/bootstrap")
			if err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			defer resp.Body.Close()
			if err := json.NewDecoder(resp.Body).Decode(&boot); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    MCP URL:        %s\n", boot.MCPURL)
			fmt.Printf("    tok_read:       alice with [read]\n")
			fmt.Printf("    tok_read_write: alice with [read write]\n")
			fmt.Printf("    tok_all:        alice with [read write admin]\n")
			fmt.Printf("    tok_bob:        bob   with [read write admin]\n")
			return
		})

	// --- Step 2: Public Discovery ---
	demo.Step("Public discovery: tools/list without a token").
		Arrow("Host", "Server", "POST /mcp — initialize + tools/list (no Authorization header)").
		DashedArrow("Server", "Host", "tool list (3 tools, even without auth)").
		Note("The server is configured with WithPublicMethods(\"initialize\", \"notifications/initialized\", \"tools/list\", \"prompts/list\", \"ping\"). These bypass the auth check so an unauthenticated client can discover what's available before requesting a token.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Mint a session (no Authorization header — initialize is public) and capture
# the session id, then call the public tools/list.
SID=$(curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | jq '.result.tools | length'`).Default(),
			demokit.MakeVariant("go", "go", `// No bearer token — tools/list is in the server's WithPublicMethods set.
c := client.NewClient(boot.MCPURL,
    core.ClientInfo{Name: "demo-host-anon", Version: "1.0"},
)
defer c.Close()
c.Connect()
tools, _ := c.ListTools() // succeeds without auth`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			c := client.NewClient(boot.MCPURL,
				core.ClientInfo{Name: "demo-host-anon", Version: "1.0"},
				client.WithTracerProvider(tp),
			)
			defer c.Close()
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			tools, err := c.ListTools()
			if err != nil {
				fmt.Printf("    UNEXPECTED: %v\n", err)
				return
			}
			fmt.Printf("    tools/list returned %d tools (no token needed):\n", len(tools))
			for _, t := range tools {
				fmt.Printf("      - %s: %s\n", t.Name, t.Description)
			}
			return
		})

	// --- Step 3: Protected method without a token → 401 ---
	demo.Step("Protected method without a token → 401").
		Arrow("Host", "Server", "tools/call: echo  (no Authorization header)").
		DashedArrow("Server", "Host", "HTTP 401 + WWW-Authenticate").
		Note("tools/call is NOT in the public allowlist. The mcpkit client surfaces this as *client.ClientAuthError. A real MCP host would use this to trigger an OAuth flow.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Same unauthenticated session ($SID from the previous step). tools/call is
# NOT public, so the server replies 401 + WWW-Authenticate. -i shows headers.
curl -s -i -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"}}}' \
  | grep -iE 'HTTP/|www-authenticate'`).Default(),
			demokit.MakeVariant("go", "go", `// The mcpkit client surfaces the 401 as a typed *client.ClientAuthError.
_, err := c.ToolCall("echo", map[string]any{"message": "hi"})
var authErr *client.ClientAuthError
if errors.As(err, &authErr) {
    // authErr.StatusCode == 401; authErr.WWWAuthenticate carries the challenge
}`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			c := client.NewClient(boot.MCPURL,
				core.ClientInfo{Name: "demo-host-anon", Version: "1.0"},
				client.WithTracerProvider(tp),
			)
			defer c.Close()
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			_, err := c.ToolCall("echo", map[string]any{"message": "hi"})
			var authErr *client.ClientAuthError
			if !errors.As(err, &authErr) {
				fmt.Printf("    UNEXPECTED error type: %T %v\n", err, err)
				return
			}
			fmt.Printf("    HTTP %d\n", authErr.StatusCode)
			fmt.Printf("    WWW-Authenticate: %s\n", authErr.WWWAuthenticate)
			return
		})

	// --- Step 4: JWT — call echo with a valid token ---
	demo.Step("Call echo with alice's read-only token (JWT validated via JWKS)").
		Arrow("Host", "Server", "tools/call: echo + Bearer alice/[read]").
		DashedArrow("Server", "Host", "echo + claims (subject=alice, scopes=[read])").
		Note("The mcpkit JWTValidator fetches the AS's JWKS, verifies the RS256 signature using kid lookup, and exposes the claims to handlers via core.AuthClaims(ctx). echo is a no-scope tool that reflects the authenticated identity back, so we can see the validated claims.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Mint a fresh session carrying alice's read token, then call echo (no scope
# required). The server validates the RS256 JWT against the AS's JWKS.
SID=$(curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $TOK_READ" \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" -H "Authorization: Bearer $TOK_READ" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" -H "Authorization: Bearer $TOK_READ" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"}}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// WithClientBearerToken attaches the JWT on every request for this client.
readClient = client.NewClient(boot.MCPURL,
    core.ClientInfo{Name: "demo-host-alice", Version: "1.0"},
    client.WithClientBearerToken(boot.TokRead),
)
readClient.Connect()
text, _ := readClient.ToolCall("echo", map[string]any{"message": "hello"})`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			readClient = client.NewClient(boot.MCPURL,
				core.ClientInfo{Name: "demo-host-alice", Version: "1.0"},
				client.WithClientBearerToken(boot.TokRead),
				client.WithTracerProvider(tp),
			)
			if err := readClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			text, err := readClient.ToolCall("echo", map[string]any{"message": "hello"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			return
		})

	// --- Step 5: Scope: missing 'write' scope → 403 ---
	demo.Step("Call write-tool with read-only token → 403 + insufficient_scope").
		Arrow("Host", "Server", "tools/call: write-tool + Bearer alice/[read]").
		DashedArrow("Server", "Host", "HTTP 403 + WWW-Authenticate: Bearer error=\"insufficient_scope\", scope=\"write\"").
		Note("write-tool declares RequiredScopes: [\"write\"] on its ToolDef. The auth.NewToolScopeMiddleware short-circuits the request with HTTP 403 + WWW-Authenticate before the handler runs (per SEP-2643 UC2 + RFC 6750). Scope info is in the header — the client's RFC 6750 parser auto-populates RequiredScopes.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Same read-token session ($SID). write-tool needs the "write" scope the token
# lacks → 403 + WWW-Authenticate: Bearer error="insufficient_scope", scope="write".
curl -s -i -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" -H "Authorization: Bearer $TOK_READ" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write-tool","arguments":{"data":"x"}}}' \
  | grep -iE 'HTTP/|www-authenticate'`).Default(),
			demokit.MakeVariant("go", "go", `// The client parses the WWW-Authenticate challenge (RFC 6750) into RequiredScopes.
_, err := readClient.ToolCall("write-tool", map[string]any{"data": "x"})
var authErr *client.ClientAuthError
if errors.As(err, &authErr) {
    // authErr.StatusCode == 403; authErr.RequiredScopes == ["write"] — drives step-up
}`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			_, err := readClient.ToolCall("write-tool", map[string]any{"data": "x"})
			var authErr *client.ClientAuthError
			if !errors.As(err, &authErr) {
				fmt.Printf("    UNEXPECTED error type: %T %v\n", err, err)
				return
			}
			fmt.Printf("    HTTP %d\n", authErr.StatusCode)
			fmt.Printf("    WWW-Authenticate: %s\n", authErr.WWWAuthenticate)
			fmt.Printf("    → RequiredScopes (parsed): %v\n", authErr.RequiredScopes)
			return
		})

	// --- Step 6: Step up to read+write → write-tool succeeds ---
	demo.Step("Reconnect with read+write token → write-tool succeeds").
		Arrow("Host", "Server", "POST /mcp — initialize + Bearer alice/[read write]").
		DashedArrow("Server", "Host", "new session").
		Arrow("Host", "Server", "tools/call: write-tool").
		DashedArrow("Server", "Host", "ok").
		Note("New session with the broader token. write-tool runs because the token includes write. Scope step-up in real systems is driven by the WWW-Authenticate response from the previous step — see examples/fine-grained-auth/ for the full SEP-2643 UC2 flow.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# New session with the read+write token; now write-tool succeeds.
SID_RW=$(curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $TOK_RW" \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID_RW" -H "Authorization: Bearer $TOK_RW" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID_RW" -H "Authorization: Bearer $TOK_RW" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write-tool","arguments":{"data":"hello write"}}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// A second client with the broader token — the read token's session is untouched.
readWriteClient = client.NewClient(boot.MCPURL,
    core.ClientInfo{Name: "demo-host-alice-rw", Version: "1.0"},
    client.WithClientBearerToken(boot.TokReadWrite),
)
readWriteClient.Connect()
text, _ := readWriteClient.ToolCall("write-tool", map[string]any{"data": "hello write"})`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			readWriteClient = client.NewClient(boot.MCPURL,
				core.ClientInfo{Name: "demo-host-alice-rw", Version: "1.0"},
				client.WithClientBearerToken(boot.TokReadWrite),
				client.WithTracerProvider(tp),
			)
			if err := readWriteClient.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			text, err := readWriteClient.ToolCall("write-tool", map[string]any{"data": "hello write"})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			fmt.Printf("    Result: %s\n", text)
			return
		})

	// --- Step 7: admin-tool also requires 'admin' scope ---
	demo.Step("admin-tool with read+write token → 403 (needs admin)").
		Arrow("Host", "Server", "tools/call: admin-tool + Bearer alice/[read write]").
		DashedArrow("Server", "Host", "HTTP 403 + WWW-Authenticate: scope=\"admin\"").
		Note("admin-tool requires \"admin\" scope. The same scope-enforcement middleware returns 403 + WWW-Authenticate with the missing scope.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Same read+write session ($SID_RW). admin-tool needs "admin" → 403 +
# WWW-Authenticate: Bearer error="insufficient_scope", scope="admin".
curl -s -i -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID_RW" -H "Authorization: Bearer $TOK_RW" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin-tool","arguments":{"action":"rotate"}}}' \
  | grep -iE 'HTTP/|www-authenticate'`).Default(),
			demokit.MakeVariant("go", "go", `// Same client, stronger requirement: admin-tool needs the "admin" scope.
_, err := readWriteClient.ToolCall("admin-tool", map[string]any{"action": "rotate"})
var authErr *client.ClientAuthError
if errors.As(err, &authErr) {
    // authErr.StatusCode == 403; missing scope "admin"
}`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			_, err := readWriteClient.ToolCall("admin-tool", map[string]any{"action": "rotate"})
			var authErr *client.ClientAuthError
			if !errors.As(err, &authErr) {
				fmt.Printf("    UNEXPECTED error type: %T %v\n", err, err)
				return
			}
			fmt.Printf("    HTTP %d\n", authErr.StatusCode)
			fmt.Printf("    WWW-Authenticate: %s\n", authErr.WWWAuthenticate)
			return
		})

	// --- Step 8: Session binding — bob's token on alice's session ---
	demo.Step("Session binding: bob's token on alice's session → rejected").
		Arrow("Host", "Server", "tools/call: echo + Mcp-Session-Id=<alice's> + Bearer bob/[all]").
		DashedArrow("Server", "Host", "HTTP 403 (subject mismatch)").
		Note("mcpkit binds the principal (Claims.Subject) to the session at creation time. Subsequent requests on the same session must come from the same subject. Even though bob's token is independently valid (correct signature, fresh, has all scopes), it doesn't match alice's bound session — so the request is rejected. This prevents an attacker who steals a session ID from using their own valid token to take over.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Replay alice's session id ($SID) but with bob's (independently valid) token.
# The session is bound to alice's subject, so the swap is rejected with 403.
curl -s -i -X POST "$MCP" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" -H "Authorization: Bearer $TOK_BOB" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hijack attempt"}}}' \
  | grep -iE 'HTTP/'`).Default(),
			demokit.MakeVariant("go", "go", `// Raw request: alice's session id + bob's token. Subject mismatch → HTTP 403.
body := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"echo\",\"arguments\":{\"message\":\"hijack attempt\"}}}"
req, _ := http.NewRequest("POST", boot.MCPURL, strings.NewReader(body))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Accept", "application/json, text/event-stream")
req.Header.Set("Mcp-Session-Id", readClient.SessionID()) // alice's bound session
req.Header.Set("Authorization", "Bearer "+boot.TokBob)    // bob's valid token
resp, _ := http.DefaultClient.Do(req) // resp.StatusCode == 403`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			sid := readClient.SessionID()
			fmt.Printf("    alice's session ID: %s\n", sid)
			body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hijack attempt"}}}`)
			req, _ := http.NewRequest("POST", boot.MCPURL, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Session-Id", sid)
			req.Header.Set("Authorization", "Bearer "+boot.TokBob)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			defer resp.Body.Close()
			fmt.Printf("    Server returned HTTP %d (session hijack prevented)\n", resp.StatusCode)
			return
		})

	demo.Section("Where each pattern lives in the code",
		"- Public methods: `server.WithPublicMethods(...)`",
		"- JWT/JWKS validation: `auth.NewJWTValidator(JWTConfig{JWKSURL: ...})` — `ext/auth/jwt_validator.go`",
		"- Per-tool scopes: `core.ToolDef.RequiredScopes` + `auth.NewToolScopeMiddleware(reg)` — `ext/auth/scope_middleware.go`",
		"- Session binding: enforced in `server/streamable_transport.go` (verifyPrincipal); subject is captured at session creation",
	)

	common.SetupRenderer(demo)

	demo.Execute()

	if readClient != nil {
		readClient.Close()
	}
	if readWriteClient != nil {
		readWriteClient.Close()
	}
}

// --- Serve mode ---

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("auth-unified"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	logger := common.NewMCPLogger("[mcp] ")

	env := authcommon.NewEnv([]string{"read", "write", "admin"})
	defer env.Close()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL,
		authcommon.WithMCPTracerProvider(tp),
		authcommon.WithOneauthTracerProvider(commonotel.UnderlyingOTelTP(tp)),
	)

	// Pre-mint tokens for the walkthrough.
	tokRead := env.MintToken("alice", []string{"read"})
	tokReadWrite := env.MintToken("alice", []string{"read", "write"})
	tokAll := env.MintToken("alice", []string{"read", "write", "admin"})
	tokBob := env.MintToken("bob", []string{"read", "write", "admin"})

	// Pre-mint deliberately-bad tokens for the SEP-2356 / MCP-auth
	// conformance suites. Each is properly signed (signature passes
	// JWKS verification) but violates one specific RFC 7519 claim.
	tokExpired := env.MintExpiredToken("alice", []string{"read"})
	tokWrongAud := env.MintWrongAudienceToken("alice", []string{"read"})
	tokWrongIss := env.MintWrongIssuerToken("alice", []string{"read"})

	// Pre-mint an upstream-IdP-style assertion the AS trusts. Used by
	// the panyam/mcpconformance Phase 3c
	// auth-enterprise-managed-token-exchange-flow-shape check as the
	// `subject_token` parameter for the RFC 8693 token-exchange grant.
	tokUpstreamAssertion := env.MintUpstreamAssertion("alice")

	log.Printf("MCP endpoint: %s/mcp", listenURL)
	log.Printf("Bootstrap:    %s/demo/bootstrap", listenURL)
	log.Printf("AS issuer:    %s", env.AS.Issuer())
	log.Printf("")
	log.Printf("Tokens (paste into Authorization: Bearer <token>):")
	log.Printf("  alice/[read]:              %s", tokRead)
	log.Printf("  alice/[read write]:        %s", tokReadWrite)
	log.Printf("  alice/[read write admin]:  %s", tokAll)
	log.Printf("  bob/[read write admin]:    %s", tokBob)

	if err := common.RunServer(common.ServerConfig{
		Name:           "auth-unified",
		Version:        "1.0",
		Addr:           *addr,
		Logger:         logger,
		TracerProvider: tp,
		Options: []server.Option{
			server.WithAuth(validator),
			server.WithPublicMethods("initialize", "notifications/initialized", "tools/list", "prompts/list", "ping"),
			server.WithMiddleware(server.ToolCallLogger(logger)),
		},
		Register: func(srv *server.Server) {
			authcommon.RegisterEchoTools(srv)
			srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry()))
		},
		TransportOptions: []server.TransportOption{
			server.WithMux(func(m *http.ServeMux) {
				// Bootstrap endpoint for the demokit walkthrough.
				m.HandleFunc("GET /demo/bootstrap", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(bootstrapInfo{
						MCPURL:               listenURL + "/mcp",
						TokRead:              tokRead,
						TokReadWrite:         tokReadWrite,
						TokAll:               tokAll,
						TokBob:               tokBob,
						TokExpired:           tokExpired,
						TokWrongAudience:     tokWrongAud,
						TokWrongIssuer:       tokWrongIss,
						TokUpstreamAssertion: tokUpstreamAssertion,
					})
				})
				auth.MountAuth(m, auth.AuthConfig{
					ResourceURI:          listenURL,
					AuthorizationServers: []string{env.AS.Issuer()},
					ScopesSupported:      env.Scopes,
					MCPPath:              "/mcp",
				})
			}),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

// Example: Session hijacking prevention.
//
// Two users with different JWT subjects. User B cannot use User A's session.
// Demonstrates automatic principal binding on Streamable HTTP transport.
//
// Run: go run ./session-binding
package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/testutil"
)

const accept = "application/json, text/event-stream"

func main() {
	fmt.Println("=== Auth Example: Session Hijacking Prevention ===")
	fmt.Println()

	// Step 1: Start AS + MCP server.
	as, err := testutil.NewAuthServer(testutil.WithScopes([]string{"read"}))
	if err != nil {
		log.Fatal(err)
	}
	defer as.Close()

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:  as.JWKSURL(),
		Issuer:   as.Issuer(),
		Audience: "", // disabled for simplicity — production should validate
	})
	validator.Start()
	defer validator.Stop()

	srv := server.NewServer(
		core.ServerInfo{Name: "session-demo", Version: "1.0"},
		server.WithAuth(validator),
	)
	srv.Register(core.TextTool[struct{}]("ping", "Returns pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			claims := ctx.AuthClaims()
			if claims != nil {
				return fmt.Sprintf("pong (user: %s)", claims.Subject), nil
			}
			return "pong", nil
		},
	))
	handler = srv.Handler(server.WithStreamableHTTP(true))

	// Step 2: Mint tokens for two different users.
	fmt.Println("Step 1: Mint tokens for alice and bob...")
	tokAlice, _ := as.MintTokenForSubject("alice", []string{"read"})
	tokBob, _ := as.MintTokenForSubject("bob", []string{"read"})
	fmt.Println("  → alice: ..." + tokAlice[len(tokAlice)-20:])
	fmt.Println("  → bob:   ..." + tokBob[len(tokBob)-20:])

	// Step 3: Alice connects and calls ping.
	fmt.Println("\nStep 2: Alice connects and calls ping...")
	cAlice := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "alice", Version: "1.0"},
		client.WithClientBearerToken(tokAlice),
	)
	if err := cAlice.Connect(); err != nil {
		fmt.Printf("  → Alice connect failed: %v\n", err)
		return
	}
	result, _ := cAlice.ToolCall("ping", nil)
	fmt.Printf("  → ping: %s ✓\n", result)

	// Step 4: Get Alice's session ID via raw HTTP for the hijack demo.
	sessionID := initSession(ts.URL, tokAlice)
	if sessionID == "" {
		fmt.Println("  → Could not get session ID (server may not return it on second init)")
		// Fall back: just demonstrate with two separate client connections
		fmt.Println("\nStep 3: Bob connects separately and calls ping...")
		cBob := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "bob", Version: "1.0"},
			client.WithClientBearerToken(tokBob),
		)
		if err := cBob.Connect(); err != nil {
			fmt.Printf("  → Bob connect failed: %v\n", err)
		} else {
			r2, _ := cBob.ToolCall("ping", nil)
			fmt.Printf("  → ping: %s (bob has own session) ✓\n", r2)
			cBob.Close()
		}
	} else {
		fmt.Printf("  → Alice's session: %s\n", sessionID)

		// Step 5: Bob tries to use Alice's session → 403.
		fmt.Println("\nStep 3: Bob tries to hijack Alice's session...")
		status := postToSession(ts.URL, tokBob, sessionID)
		fmt.Printf("  → HTTP %d", status)
		if status == 403 {
			fmt.Print(" (Forbidden — session hijacking prevented!) ✓")
		}
		fmt.Println()
	}

	cAlice.Close()
	fmt.Println("\n=== Done ===")
}

func initSession(base, token string) string {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Mcp-Session-Id")
}

func postToSession(base, token, sessionID string) int {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

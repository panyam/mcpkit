// Example: URL-mode elicitation with consent approval (UC1).
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # starts the MCP server on :8080
//	Terminal 2:  make run           # runs the demokit client (scripted MCP host)
//
// The server is a real MCP server that any host can connect to.
// The demokit client acts as a scripted host walking through the UC1 flow.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
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

	demo := demokit.New("URL Elicitation — Consent Approval Flow (UC1)").
		Dir("elicitation").
		Description("A scripted MCP host walking through the UC1 consent approval flow.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("Browser", "User Browser"),
		)

	demo.Section("Setup",
		"Before running this demo, start the MCP server in a separate terminal:",
		"",
		"```",
		"Terminal 1:  make serve        # start the MCP server on :8080",
		"Terminal 2:  make run          # run this demo",
		"```",
	)

	var (
		c         *client.Client
		contextID string // authorizationContextId from the -32042 response

		// Channel signaled when notifications/elicitation/complete arrives.
		approved = make(chan string, 1)
	)

	// --- Step 1: Connect to server ---
	demo.Step("Connect to the MCP server and initialize session").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + Mcp-Session-Id").
		Arrow("Host", "Server", "GET /mcp — open SSE stream for notifications").
		Note("Connect with a notification callback listening for notifications/elicitation/complete. The GET SSE stream receives server-pushed notifications.").
		Run(func() {
			fmt.Printf("    Connecting to %s ...\n", serverURL)

			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithNotificationCallback(func(method string, params any) {
					if method == "notifications/elicitation/complete" {
						fmt.Fprintf(os.Stderr, "    ✓ Received notification: %s\n", method)
						// Extract elicitationId from params.
						raw, _ := json.Marshal(params)
						var p struct {
							ElicitationID string `json:"elicitationId"`
						}
						json.Unmarshal(raw, &p)
						approved <- p.ElicitationID
					}
				}),
				client.WithGetSSEStream(),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				fmt.Printf("    Start the server with: make serve\n")
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			fmt.Printf("    SSE notification stream active\n\n")

			tools, _ := c.ListTools()
			fmt.Printf("    Tools:\n")
			for _, t := range tools {
				fmt.Printf("      - %s: %s\n", t.Name, t.Description)
			}
		})

	// --- Step 2: Call tool — get denied ---
	demo.Step("Call access_protected_resource — denied with consent URL").
		Arrow("Host", "Server", "tools/call: access_protected_resource").
		DashedArrow("Server", "Host", "error -32042 + consent URL + authzContextId").
		Note("The consent middleware intercepts the call and returns -32042 (URLElicitationRequired) with a URL the user must visit to approve access.").
		Run(func() {
			_, err := c.ToolCall("access_protected_resource", map[string]any{"resourceId": "my-doc"})

			var rpcErr *client.RPCError
			if !errors.As(err, &rpcErr) {
				fmt.Printf("    UNEXPECTED: %v\n", err)
				return
			}

			// Show the full error response.
			errJSON, _ := json.MarshalIndent(map[string]any{
				"code":    rpcErr.Code,
				"message": rpcErr.Message,
				"data":    rpcErr.Data,
			}, "    ", "  ")
			fmt.Printf("    Response error:\n    %s\n\n", errJSON)

			// Parse the error data to extract consent URL and context ID.
			var data struct {
				Authorization struct {
					AuthorizationContextID string `json:"authorizationContextId"`
				} `json:"authorization"`
				Elicitations []struct {
					Mode    string `json:"mode"`
					Message string `json:"message"`
					URL     string `json:"url"`
				} `json:"elicitations"`
			}
			raw, _ := json.Marshal(rpcErr.Data)
			json.Unmarshal(raw, &data)

			contextID = data.Authorization.AuthorizationContextID
			if len(data.Elicitations) > 0 {
				fmt.Printf("    Consent URL: %s\n", data.Elicitations[0].URL)
			}
		})

	// --- Step 3: Open browser, wait for notification, auto-retry ---
	demo.Step("Open consent URL → wait for approval notification → auto-retry").
		Arrow("Host", "Browser", "open consent URL").
		Arrow("Browser", "Server", "POST /approve?ctx=...").
		DashedArrow("Server", "Host", "notifications/elicitation/complete (via SSE)").
		Arrow("Host", "Server", "tools/call + _meta.authorizationContextId (auto-retry)").
		DashedArrow("Server", "Host", "Access granted to resource").
		Note("The host opens the consent URL and waits for the server to send a notifications/elicitation/complete notification via the SSE stream. When it arrives, the host automatically retries with the authorizationContextId.").
		Run(func() {
			approveURL := fmt.Sprintf("%s/approve?ctx=%s", serverURL, contextID)
			fmt.Printf("    Opening browser: %s\n", approveURL)
			openBrowser(approveURL)
			fmt.Printf("    Waiting for notifications/elicitation/complete ...\n\n")

			// Wait for the approval notification (with timeout).
			select {
			case elicitID := <-approved:
				fmt.Printf("    ✓ Approval notification received (elicitationId: %s)\n", elicitID)
				fmt.Printf("    Auto-retrying with authorizationContextId ...\n\n")
			case <-time.After(2 * time.Minute):
				fmt.Printf("    ✗ Timed out waiting for approval (2 min). Did you click Approve?\n")
				return
			}

			// Retry with the context ID.
			result, err := c.Call("tools/call", map[string]any{
				"name":      "access_protected_resource",
				"arguments": map[string]any{"resourceId": "my-doc"},
				"_meta": map[string]any{
					"modelcontextprotocol.io/authorizationContextId": contextID,
				},
			})
			if err != nil {
				fmt.Printf("    UNEXPECTED error: %v\n", err)
				return
			}
			var toolResult core.ToolResult
			result.Unmarshal(&toolResult)
			fmt.Printf("    Result: %s\n", toolResult.Content[0].Text)
		})

	// Use TUI renderer if --tui flag is passed.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	cmd.Start()
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
	consent := newConsentStore()

	logger := demokit.NewColorLogger("[mcp] ", []demokit.ColorRule{
		{Contains: "error=", DarkColor: demokit.ANSIRed},
		{Contains: "ERROR", DarkColor: demokit.ANSIRed},
		{Contains: "[http] →", DarkColor: demokit.ANSIGray, LightColor: demokit.ANSIDimBlue},
		{Contains: "[http] ←", DarkColor: demokit.ANSICyan, LightColor: demokit.ANSIBlue},
		{Contains: "MCP ", DarkColor: demokit.ANSIBrightGreen, LightColor: demokit.ANSIGreen},
	})
	srv := server.NewServer(core.ServerInfo{
		Name:    "elicitation-example",
		Version: "1.0.0",
	},
		server.WithRequestLogging(logger),
		server.WithMiddleware(server.LoggingMiddleware(logger)),
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "access_protected_resource",
			Description: "Access a protected resource. Requires user approval via URL consent flow on first call.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"resourceId": {"type": "string", "description": "Resource to access"}
				}
			}`),
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				ResourceID string `json:"resourceId"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.ResourceID == "" {
				args.ResourceID = "default-resource"
			}
			return core.TextResult(fmt.Sprintf(
				"Access granted to resource %q (approved via consent)", args.ResourceID)), nil
		},
	)

	srv.UseMiddleware(consentMiddleware(consent, listenURL))

	mux := http.NewServeMux()
	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)
	mux.Handle("/mcp", cors(srv.Handler(server.WithStreamableHTTP(true))))
	mux.HandleFunc("/approve", consent.handleApprove)

	fmt.Printf("Elicitation example server on %s\n", addr)
	fmt.Printf("MCP endpoint: %s/mcp\n", listenURL)
	fmt.Printf("Tools: access_protected_resource\n")
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

// --- Consent middleware + store ---

func consentMiddleware(store *consentStore, baseURL string) server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) *core.Response {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		var envelope struct {
			Name string `json:"name"`
			Meta *struct {
				AuthzContextID string `json:"modelcontextprotocol.io/authorizationContextId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req)
		}

		if envelope.Name != "access_protected_resource" {
			return next(ctx, req)
		}

		// Check if retrying with an approved context ID (FineGrainedAuth-aware clients).
		if envelope.Meta != nil && envelope.Meta.AuthzContextID != "" {
			if store.isApproved(envelope.Meta.AuthzContextID) {
				return next(ctx, req)
			}
		}

		// Fallback: check if this session+tool was already approved.
		// Supports clients (VS Code, Claude Desktop) that retry without the context ID.
		sessionKey := core.GetSessionID(ctx) + ":" + envelope.Name
		if store.isSessionApproved(sessionKey) {
			return next(ctx, req)
		}

		contextID := generateID("authzctx")
		elicitID := generateID("el")

		bgCtx := core.DetachForBackground(ctx)
		store.addPending(contextID, elicitID, sessionKey, func(method string, params any) {
			core.Notify(bgCtx, method, params)
		})

		approveURL := fmt.Sprintf("%s/approve?ctx=%s", baseURL, contextID)

		return core.NewErrorResponseWithData(req.ID,
			core.ErrCodeURLElicitationRequired,
			"You need to approve access to this resource.",
			map[string]any{
				"authorization": core.AuthorizationDenial{
					Reason:                 "insufficient_authorization",
					AuthorizationContextID: contextID,
				},
				"elicitations": []core.ElicitationRequest{
					{
						Mode:          core.ElicitModeURL,
						Message:       "Open this page to approve access to the requested resource.",
						URL:           approveURL,
						ElicitationID: elicitID,
					},
				},
			},
		)
	}
}

type consentStore struct {
	mu              sync.Mutex
	pending         map[string]*consentEntry
	approved        map[string]bool
	sessionApproved map[string]bool
	pendingSession  map[string]string
}

type consentEntry struct {
	elicitationID string
	notifyFunc    core.NotifyFunc
}

func newConsentStore() *consentStore {
	return &consentStore{
		pending:         make(map[string]*consentEntry),
		approved:        make(map[string]bool),
		sessionApproved: make(map[string]bool),
		pendingSession:  make(map[string]string),
	}
}

func (s *consentStore) addPending(contextID, elicitID string, sessionKey string, nf core.NotifyFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[contextID] = &consentEntry{elicitationID: elicitID, notifyFunc: nf}
	if sessionKey != "" {
		s.pendingSession[contextID] = sessionKey
	}
}

func (s *consentStore) isApproved(contextID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[contextID]
}

func (s *consentStore) isSessionApproved(sessionKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionApproved[sessionKey]
}

func (s *consentStore) approve(contextID string) (*consentEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.pending[contextID]
	if !ok {
		return nil, false
	}
	delete(s.pending, contextID)
	s.approved[contextID] = true
	if sessionKey, ok := s.pendingSession[contextID]; ok {
		s.sessionApproved[sessionKey] = true
		delete(s.pendingSession, contextID)
	}
	return entry, true
}

func (s *consentStore) handleApprove(w http.ResponseWriter, r *http.Request) {
	contextID := r.URL.Query().Get("ctx")
	if contextID == "" {
		http.Error(w, "missing ctx parameter", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodPost {
		entry, ok := s.approve(contextID)
		if !ok {
			http.Error(w, "unknown or already approved context", http.StatusNotFound)
			return
		}
		if entry.notifyFunc != nil {
			entry.notifyFunc("notifications/elicitation/complete",
				core.ElicitationCompleteParams{ElicitationID: entry.elicitationID})
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<h2>Access Approved</h2>
			<p>You can close this page. The MCP client will retry automatically.</p>
		</body></html>`)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body>
		<h2>Approve Access</h2>
		<p>An MCP tool is requesting access to a protected resource.</p>
		<p>Context: <code>%s</code></p>
		<form method="POST" action="/approve?ctx=%s">
			<button type="submit" style="padding:10px 20px;font-size:16px;cursor:pointer">
				Approve
			</button>
		</form>
	</body></html>`, contextID, contextID)
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

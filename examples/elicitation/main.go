// Example: URL-mode elicitation with consent approval (UC1).
//
// Demonstrates the FineGrainedAuth UC1 pattern where a tool requires
// user approval via an out-of-band URL. The MCP client's bearer token
// remains unchanged — the user just needs to approve access through
// the server's consent UI.
//
// Run interactively:
//
//	go run .
//
// Generate README:
//
//	go run . --readme > README.md
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

func main() {
	// --serve mode: start the MCP server standalone for use with MCPJam or other clients.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}

	demo := demokit.New("URL Elicitation — Consent Approval Flow (UC1)").
		Dir("elicitation").
		Description("Demonstrates the FineGrainedAuth UC1 pattern: a tool requires out-of-band user approval via a URL before granting access.").
		Actors(
			demokit.Actor("Client", "MCP Client"),
			demokit.Actor("Server", "MCP Server"),
			demokit.Actor("Browser", "User Browser"),
		)

	var (
		baseURL   string
		sessionID string
		contextID string // authorizationContextId from the -32042 response
	)

	// --- Step 1: Start server ---
	demo.Step("Start the MCP server with consent middleware").
		Note("The server has one tool protected by consent middleware. First calls get rejected with -32042 until the user approves via a URL.").
		Run(func() {
			addr, err := startServer()
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			baseURL = "http://" + addr
			fmt.Printf("    Server started at %s\n", baseURL)
			fmt.Printf("    MCP endpoint: %s/mcp\n\n", baseURL)

			// Show the registered tool definition.
			toolDef, _ := json.MarshalIndent(map[string]any{
				"name":        "access_protected_resource",
				"description": "Access a protected resource. Requires user approval via URL consent flow on first call.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"resourceId": map[string]any{"type": "string", "description": "Resource to access"},
					},
				},
			}, "    ", "  ")
			fmt.Printf("    Registered tool:\n    %s\n", toolDef)
		})

	// --- Step 2: Initialize session ---
	demo.Step("Initialize MCP session").
		Arrow("Client", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Client", "serverInfo + Mcp-Session-Id").
		Note("The client sends an initialize request and receives a session ID for subsequent calls.").
		Run(func() {
			initParams := map[string]any{
				"protocolVersion": "2025-03-26",
				"clientInfo":      map[string]any{"name": "demo-client", "version": "1.0"},
				"capabilities":    map[string]any{},
			}

			// Show the request.
			reqJSON, _ := json.MarshalIndent(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params":  initParams,
			}, "    ", "  ")
			fmt.Printf("    Request:\n    %s\n\n", reqJSON)

			resp, err := mcpCall(baseURL, "", "initialize", initParams)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			sessionID = resp.sessionID

			// Show the response.
			var resultAny any
			json.Unmarshal(resp.result, &resultAny)
			respJSON, _ := json.MarshalIndent(resultAny, "    ", "  ")
			fmt.Printf("    Response (Mcp-Session-Id: %s):\n    %s\n\n", sessionID, respJSON)

			// Send initialized notification.
			mcpNotify(baseURL, sessionID, "notifications/initialized", nil)
			fmt.Printf("    Sent notifications/initialized\n")
		})

	// --- Step 3: Call tool — get denied ---
	demo.Step("Call access_protected_resource — denied with consent URL").
		Arrow("Client", "Server", "tools/call: access_protected_resource").
		DashedArrow("Server", "Client", "error -32042 + consent URL + authzContextId").
		Note("The consent middleware intercepts the call and returns -32042 (URLElicitationRequired) with a URL the user must visit to approve access.").
		Run(func() {
			resp, err := mcpCall(baseURL, sessionID, "tools/call", map[string]any{
				"name":      "access_protected_resource",
				"arguments": map[string]any{"resourceId": "my-doc"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			if resp.rpcError == nil {
				fmt.Printf("    UNEXPECTED: tool call succeeded (expected -32042)\n")
				return
			}

			// Show the full JSON-RPC error response.
			errJSON, _ := json.MarshalIndent(map[string]any{
				"code":    resp.rpcError.Code,
				"message": resp.rpcError.Message,
				"data":    resp.rpcError.Data,
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
			raw, _ := json.Marshal(resp.rpcError.Data)
			json.Unmarshal(raw, &data)

			contextID = data.Authorization.AuthorizationContextID
			if len(data.Elicitations) > 0 {
				fmt.Printf("    Consent URL: %s\n", data.Elicitations[0].URL)
			}
		})

	// --- Step 4: Open browser for approval ---
	demo.Step("Open consent URL in browser — user clicks Approve").
		Arrow("Client", "Browser", "open consent URL").
		Arrow("Browser", "Server", "GET /approve?ctx=...").
		DashedArrow("Server", "Browser", "approval form").
		Arrow("Browser", "Server", "POST /approve?ctx=...").
		DashedArrow("Server", "Browser", "Access Approved").
		Note("The consent URL opens in the user's default browser. Click 'Approve' to grant access, then return here and press Enter to continue.").
		Run(func() {
			approveURL := fmt.Sprintf("%s/approve?ctx=%s", baseURL, contextID)
			fmt.Printf("    Opening browser: %s\n", approveURL)
			openBrowser(approveURL)
			fmt.Printf("    → Click 'Approve' in the browser, then press Enter here to continue.\n")
		})

	// --- Step 5: Retry with context ID ---
	demo.Step("Retry with authorizationContextId — access granted").
		Arrow("Client", "Server", "tools/call + _meta.authorizationContextId").
		DashedArrow("Server", "Client", "Access granted to resource").
		Note("The client retries the same tool call, this time including the authorizationContextId in _meta. The middleware recognizes the approved context and lets the call through.").
		Run(func() {
			resp, err := mcpCall(baseURL, sessionID, "tools/call", map[string]any{
				"name":      "access_protected_resource",
				"arguments": map[string]any{"resourceId": "my-doc"},
				"_meta": map[string]any{
					"modelcontextprotocol.io/authorizationContextId": contextID,
				},
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

	// Use TUI renderer if --tui flag is passed.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()
}

// serve starts the MCP server standalone (no demokit, no demo steps).
// Connect an MCP host to http://localhost:<port>/mcp.
func serve() {
	addr := ":8080"
	for i, arg := range os.Args[1:] {
		if arg == "--addr" && i+2 < len(os.Args) {
			addr = os.Args[i+2]
		}
	}
	listenURL := fmt.Sprintf("http://localhost%s", addr)
	consent := newConsentStore()

	srv := server.NewServer(core.ServerInfo{
		Name:    "elicitation-example",
		Version: "1.0.0",
	})

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

// --- MCP HTTP helpers ---

type mcpResponse struct {
	sessionID string
	result    json.RawMessage
	rpcError  *core.Error
}

// mcpCall sends a JSON-RPC request to the MCP server over Streamable HTTP
// and parses the SSE response.
func mcpCall(baseURL, sessionID, method string, params any) (*mcpResponse, error) {
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
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		sid = sessionID
	}

	// Parse SSE response — look for "data:" lines.
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

// mcpNotify sends a JSON-RPC notification (no response expected).
func mcpNotify(baseURL, sessionID, method string, params any) {
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
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
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

// --- Server setup (same consent logic, started in background) ---

func startServer() (string, error) {
	consent := newConsentStore()

	srv := server.NewServer(core.ServerInfo{
		Name:    "elicitation-example",
		Version: "1.0.0",
	})

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	listenURL := "http://" + addr

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

	go http.Serve(ln, mux)
	return addr, nil
}

// --- Consent middleware + store (unchanged from standalone server) ---

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
	mu       sync.Mutex
	pending  map[string]*consentEntry
	approved map[string]bool

	// sessionApproved tracks approvals by session ID, so clients that
	// don't send authorizationContextId on retry (e.g., VS Code) still work.
	// Key: "sessionID:toolName"
	sessionApproved map[string]bool
	// pendingSession maps contextID → "sessionID:toolName" for session-level approval on POST /approve.
	pendingSession map[string]string
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
	// Also approve by session so clients that don't send the context ID on retry still work.
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

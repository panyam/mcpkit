// Example: URL-mode elicitation with consent approval (UC1).
//
// Demonstrates the FineGrainedAuth UC1 pattern where a tool requires
// user approval via an out-of-band URL. The MCP client's bearer token
// remains unchanged — the user just needs to approve access through
// the server's consent UI.
//
// Flow:
//  1. Client calls access_protected_resource
//  2. Middleware returns -32042 (URLElicitationRequired) with a consent URL
//  3. User visits the URL in their browser and clicks "Approve"
//  4. Server sends notifications/elicitation/complete
//  5. Client retries with authorizationContextId in _meta → succeeds
//
// Run:
//
//	go run . -addr :8086
//
// Then connect an MCP host to http://localhost:8086/mcp and call
// the access_protected_resource tool.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8086", "listen address")
	flag.Parse()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	consent := newConsentStore()

	srv := server.NewServer(core.ServerInfo{
		Name:    "elicitation-example",
		Version: "1.0.0",
	})

	// Tool: access_protected_resource — the actual business logic.
	// Only reached if consent middleware has approved the request.
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

	// Consent middleware: intercepts tools/call for access_protected_resource.
	// If not yet approved, returns -32042 with a consent URL.
	// If approved (authorizationContextId in _meta), passes through.
	srv.UseMiddleware(consentMiddleware(consent, listenURL))

	// HTTP mux: MCP endpoint + consent approval page.
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	mux.HandleFunc("/approve", consent.handleApprove)

	log.Printf("Elicitation example server on %s", *addr)
	log.Printf("MCP endpoint: %s/mcp", listenURL)
	log.Printf("")
	log.Printf("Tools:")
	log.Printf("  access_protected_resource — requires URL-based consent approval")
	log.Printf("")
	log.Printf("Flow:")
	log.Printf("  1. Call access_protected_resource -> gets -32042 with consent URL")
	log.Printf("  2. Visit the URL in your browser and click Approve")
	log.Printf("  3. Retry the tool call -> succeeds")

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// consentMiddleware returns middleware that enforces URL-based consent
// for the access_protected_resource tool. First call returns -32042;
// retry with the authorizationContextId passes through.
func consentMiddleware(store *consentStore, baseURL string) server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) *core.Response {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		// Parse tool name and _meta.
		var envelope struct {
			Name string `json:"name"`
			Meta *struct {
				AuthzContextID string `json:"modelcontextprotocol.io/authorizationContextId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req)
		}

		// Only intercept our protected tool.
		if envelope.Name != "access_protected_resource" {
			return next(ctx, req)
		}

		// Check if retrying with an approved context.
		if envelope.Meta != nil && envelope.Meta.AuthzContextID != "" {
			if store.isApproved(envelope.Meta.AuthzContextID) {
				return next(ctx, req) // Approved — let the tool run.
			}
		}

		// Not approved — generate consent URL and return -32042.
		contextID := generateID("authzctx")
		elicitID := generateID("el")

		// Capture the session's notify function for the completion notification.
		// core.Notify uses the session context, so we wrap it in a closure
		// that captures ctx. Use core.DetachForBackground to survive past
		// this request's lifetime.
		bgCtx := core.DetachForBackground(ctx)
		store.addPending(contextID, elicitID, func(method string, params any) {
			core.Notify(bgCtx, method, params)
		})

		approveURL := fmt.Sprintf("%s/approve?ctx=%s", baseURL, contextID)

		// Return composed -32042: URL elicitation + authorization denial envelope.
		// EXPERIMENTAL: The authorization field is additive metadata from the
		// FineGrainedAuth proposal. Type names and wire format may change.
		return core.NewErrorResponseWithData(req.ID,
			core.ErrCodeURLElicitationRequired,
			"You need to approve access to this resource.",
			map[string]any{
				"authorization": core.AuthorizationDenial{
					Reason:                 "insufficient_authorization", // EXPERIMENTAL: value will be standardized
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

// --- Consent store ---

type consentStore struct {
	mu       sync.Mutex
	pending  map[string]*consentEntry
	approved map[string]bool
}

type consentEntry struct {
	elicitationID string
	notifyFunc    core.NotifyFunc
}

func newConsentStore() *consentStore {
	return &consentStore{
		pending:  make(map[string]*consentEntry),
		approved: make(map[string]bool),
	}
}

func (s *consentStore) addPending(contextID, elicitID string, nf core.NotifyFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[contextID] = &consentEntry{elicitationID: elicitID, notifyFunc: nf}
}

func (s *consentStore) isApproved(contextID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[contextID]
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
	return entry, true
}

// handleApprove serves the consent approval page and processes approval POSTs.
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
		// Send completion notification to the client.
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

	// GET: show the approval form.
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

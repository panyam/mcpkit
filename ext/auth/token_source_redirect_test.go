package auth

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/panyam/oneauth/client"
)

// strictRedirectAS is a whole MCP authorization server in one httptest
// server: protected-resource metadata, AS metadata, DCR, an authorize
// endpoint that redirects at once, and a token endpoint. Its authorize
// endpoint compares redirect_uri to the registered ones byte for byte, port
// included. RFC 8252 §7.3 asks servers to accept any loopback port, and
// metronome-mcp.fly.dev, the SEP-3415 reference server, does not; this is
// that server's behavior in miniature.
type strictRedirectAS struct {
	*httptest.Server
	mu         sync.Mutex
	registered map[string][]string // client_id -> redirect_uris
	rejected   []string            // redirect_uris refused at /authorize
	nextID     int
}

func newStrictRedirectAS(t *testing.T) *strictRedirectAS {
	t.Helper()
	as := &strictRedirectAS{registered: map[string][]string{}}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, as.URL))
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"resource":              as.URL + "/mcp",
			"authorization_servers": []string{as.URL},
			"scopes_supported":      []string{"mcp"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer":                                as.URL,
			"authorization_endpoint":                as.URL + "/authorize",
			"token_endpoint":                        as.URL + "/token",
			"registration_endpoint":                 as.URL + "/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req client.ClientRegistrationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		as.mu.Lock()
		as.nextID++
		id := fmt.Sprintf("client-%d", as.nextID)
		as.registered[id] = req.RedirectURIs
		as.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{"client_id": id, "redirect_uris": req.RedirectURIs})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		as.mu.Lock()
		ok := slices.Contains(as.registered[q.Get("client_id")], redirect)
		if !ok {
			as.rejected = append(as.rejected, redirect)
		}
		as.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "error_description": "redirect_uri is not registered"})
			return
		}
		u, _ := url.Parse(redirect)
		u.RawQuery = url.Values{"code": {"code-1"}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600, "scope": "mcp"})
	})
	as.Server = httptest.NewServer(mux)
	t.Cleanup(as.Close)
	return as
}

// A source using DCR must register the redirect URI it will actually listen
// on. It used to register http://127.0.0.1:0/callback and then listen on
// http://localhost:<random>/callback, which an authorization server that
// matches redirect URIs exactly refuses at /authorize.
func TestOAuthTokenSource_DCRRegistersTheRedirectItListensOn(t *testing.T) {
	as := newStrictRedirectAS(t)
	src := &OAuthTokenSource{
		ServerURL:     as.URL + "/mcp",
		EnableDCR:     true,
		Scopes:        []string{"mcp"},
		AllowInsecure: true,
		OpenBrowser:   followOrFail,
	}
	tok, err := src.Token()
	if err != nil {
		as.mu.Lock()
		defer as.mu.Unlock()
		t.Fatalf("Token() = %v; /authorize refused redirect_uri %v, registered %v", err, as.rejected, as.registered)
	}
	if tok != "at-1" {
		t.Errorf("token = %q, want at-1", tok)
	}
}

// A second login with the same cached registration must reuse the same
// redirect, since that is the only one the server knows for the client.
func TestOAuthTokenSource_ReloginReusesTheRegisteredRedirect(t *testing.T) {
	as := newStrictRedirectAS(t)
	src := &OAuthTokenSource{
		ServerURL:     as.URL + "/mcp",
		EnableDCR:     true,
		Scopes:        []string{"mcp"},
		AllowInsecure: true,
		OpenBrowser:   followOrFail,
	}
	if _, err := src.Token(); err != nil {
		t.Fatalf("first Token() = %v", err)
	}
	src.Invalidate()
	if _, err := src.Token(); err != nil {
		t.Fatalf("Token() after Invalidate = %v", err)
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.registered) != 1 {
		t.Errorf("registered %d clients, want 1 (the cached registration is reused)", len(as.registered))
	}
	if len(as.rejected) != 0 {
		t.Errorf("/authorize refused %v", as.rejected)
	}
}

// CallbackPort pins the loopback port, so a caller can register its own
// redirect (DCRMeta) or pre-register a client against a known URI.
func TestOAuthTokenSource_CallbackPortPinsTheRedirect(t *testing.T) {
	as := newStrictRedirectAS(t)
	port := freeLoopbackPortForTest(t)
	src := &OAuthTokenSource{
		ServerURL:     as.URL + "/mcp",
		EnableDCR:     true,
		CallbackPort:  port,
		Scopes:        []string{"mcp"},
		AllowInsecure: true,
		OpenBrowser:   followOrFail,
	}
	if _, err := src.Token(); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	want := fmt.Sprintf("http://localhost:%d/callback", port)
	for id, uris := range as.registered {
		if !slices.Contains(uris, want) {
			t.Errorf("client %s registered %v, want %s", id, uris, want)
		}
	}
}

// followOrFail stands in for the browser like oneauth's FollowRedirects, but
// fails when the authorization server answers with an error instead of
// redirecting. FollowRedirects returns nil there and the login waits out its
// five-minute timeout, which is how the redirect bug used to look.
func followOrFail(authURL string) error {
	resp, err := http.Get(authURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("authorize answered %s", resp.Status)
	}
	return nil
}

func freeLoopbackPortForTest(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The chosen callback port stays bound by the source between logins, so a
// second process asking the kernel for a free port cannot be handed it. Two
// upstream auth scenarios run as parallel testclient processes; when the
// source picked a port and released it, one of them sometimes bound the
// other's port and its login failed (auth/dpop-nonce, CI on Linux).
func TestOAuthTokenSource_HoldsItsCallbackPortBetweenLogins(t *testing.T) {
	as := newStrictRedirectAS(t)
	src := &OAuthTokenSource{
		ServerURL:     as.URL + "/mcp",
		EnableDCR:     true,
		Scopes:        []string{"mcp"},
		AllowInsecure: true,
		OpenBrowser:   followOrFail,
	}
	t.Cleanup(func() { src.Close() })
	if _, err := src.Token(); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	src.mu.Lock()
	port := src.callbackPort
	src.mu.Unlock()
	if l, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port)); err == nil {
		l.Close()
		t.Fatalf("port %d was free after the login; another process could take it before the next one", port)
	}
	src.Invalidate()
	if _, err := src.Token(); err != nil {
		t.Fatalf("second login on the held port = %v", err)
	}
}

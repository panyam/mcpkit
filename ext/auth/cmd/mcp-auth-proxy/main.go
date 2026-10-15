// Command mcp-auth-proxy fronts an OAuth-protected MCP server with an
// unauthenticated loopback endpoint, so a tool that cannot send a bearer token
// (the conformance runner) can grade the server behind it.
//
//	go run ./cmd/mcp-auth-proxy -server https://metronome-mcp.fly.dev/mcp -addr 127.0.0.1:18201
//
// Tokens come from auth.OAuthTokenSource, which runs the MCP authorization
// flow on oneauth's client primitives: protected-resource and AS discovery,
// dynamic client registration (RegisterClient), and the authorization-code
// grant with PKCE (LoginWithBrowser). LoginWithBrowser hands the
// authorization URL to an OpenBrowser hook. With -headless (the default) that
// hook is approveHeadless, which plays the user clicking Approve; with
// -headless=false it opens a real browser, which a real login page needs.
//
// A 401 from the server triggers a fresh registration and login before the
// request is retried once. That covers a server that restarted and forgot its
// clients, not only an expired token.
package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/panyam/mcpkit/ext/auth"
)

type proxy struct {
	server   string
	scopes   []string
	headless bool

	mu  sync.Mutex
	src *auth.OAuthTokenSource
}

func (p *proxy) newSource() *auth.OAuthTokenSource {
	s := &auth.OAuthTokenSource{ServerURL: p.server, EnableDCR: true, Scopes: p.scopes}
	if p.headless {
		s.OpenBrowser = func(authURL string) error {
			err := approveHeadless(authURL)
			if err != nil {
				log.Printf("headless authorization failed: %v", err)
			}
			return err
		}
	}
	return s
}

// bearer returns a token from the current source. fresh replaces the source
// first, so the next token comes from a new registration and login. That is
// the recovery for a server that restarted and forgot its clients:
// Invalidate would keep the cached registration, which the server no longer
// knows.
func (p *proxy) bearer(fresh bool) (string, error) {
	p.mu.Lock()
	if fresh || p.src == nil {
		p.src = p.newSource()
	}
	src := p.src
	p.mu.Unlock()
	return src.Token()
}

// RoundTrip adds the bearer token, and on a 401 logs in from scratch and
// retries once. The body is buffered so the retry can resend it.
func (p *proxy) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
	}
	send := func(fresh bool) (*http.Response, error) {
		tok, err := p.bearer(fresh)
		if err != nil {
			return nil, err
		}
		r := req.Clone(req.Context())
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		return http.DefaultTransport.RoundTrip(r)
	}
	resp, err := send(false)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	resp.Body.Close()
	log.Printf("401 from %s; registering and logging in again", p.server)
	return send(true)
}

func main() {
	server := flag.String("server", "https://metronome-mcp.fly.dev/mcp", "MCP endpoint to front")
	addr := flag.String("addr", "127.0.0.1:18201", "loopback address to listen on")
	scope := flag.String("scope", "mcp", "scope to request; empty defers to the server's challenge")
	headless := flag.Bool("headless", true, "follow the authorization redirect instead of opening a browser")
	flag.Parse()

	target, err := url.Parse(*server)
	if err != nil {
		log.Fatalf("bad -server: %v", err)
	}
	p := &proxy{server: *server, headless: *headless}
	if *scope != "" {
		p.scopes = []string{*scope}
	}
	if _, err := p.bearer(false); err != nil {
		log.Fatalf("login to %s failed: %v", *server, err)
	}

	// Origin only: the client's own request path (/mcp) is passed through as is.
	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: target.Scheme, Host: target.Host})
	base := rp.Director
	rp.Director = func(r *http.Request) {
		base(r)
		r.Host = target.Host
		r.Header.Del("Authorization")
	}
	rp.Transport = p
	rp.FlushInterval = -1 // stream SSE frames as they arrive

	log.Printf("proxying http://%s%s -> %s", *addr, target.Path, *server)
	log.Fatal(http.ListenAndServe(*addr, rp))
}

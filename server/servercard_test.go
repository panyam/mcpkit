package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

var testCard = []ServerCardOption{
	WithCardName("io.github.panyam/card-test"),
	WithCardDescription("Server Card test fixture"),
}

func cardServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	info := core.Implementation{Name: "card-test", Title: "Card Test", Version: "1.4.0", WebsiteURL: "https://example.com/card"}
	return NewServer(info, append([]Option{WithServerCard(testCard...)}, opts...)...)
}

// getCard sends method to url with Host and extra headers, and returns the
// response with its body read.
func getCard(t *testing.T, method, url, host string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Accept", core.ServerCardMediaType)
	if host != "" {
		req.Host = host
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func decodeCard(t *testing.T, body []byte) core.ServerCard {
	t.Helper()
	var card core.ServerCard
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatalf("card is not JSON: %v\n%s", err, body)
	}
	return card
}

func upstreamCardSchema(t *testing.T) *core.CompiledSchema {
	t.Helper()
	raw, err := os.ReadFile("../core/testdata/servercard/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	schema["$ref"] = "#/$defs/ServerCard"
	cs, err := core.CompileSchema(schema)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestServerCard_Served(t *testing.T) {
	srv := cardServer(t)
	srv.SetCardExtensionInfo("io.modelcontextprotocol/events", func(remoteURL string) map[string]any {
		return map[string]any{"webhooks": map[string]any{"egressIpRanges": []any{"203.0.113.0/24"}}}
	})
	srv.SetCardExtensionInfo("example.com/dropped", func(string) map[string]any { return nil })
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	resp, body := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != core.ServerCardMediaType {
		t.Errorf("Content-Type %q", ct)
	}
	if verr := upstreamCardSchema(t).Validate(body); verr != nil {
		t.Fatalf("card fails the upstream schema: %v\n%s", verr, body)
	}

	card := decodeCard(t, body)
	if card.Name != "io.github.panyam/card-test" || card.Version != "1.4.0" || card.Title != "Card Test" ||
		card.WebsiteURL != "https://example.com/card" || card.Description != "Server Card test fixture" {
		t.Errorf("identity fields: %+v", card)
	}
	if len(card.Remotes) != 1 {
		t.Fatalf("remotes: %+v", card.Remotes)
	}
	r := card.Remotes[0]
	if r.Type != core.RemoteTypeStreamableHTTP || r.URL != ts.URL+"/mcp" {
		t.Errorf("remote %s %s, want streamable-http %s/mcp", r.Type, r.URL, ts.URL)
	}
	if _, ok := r.ExtensionsInfo["io.modelcontextprotocol/events"]; !ok {
		t.Errorf("events extensionsInfo missing: %+v", r.ExtensionsInfo)
	}
	if _, ok := r.ExtensionsInfo["example.com/dropped"]; ok {
		t.Error("a provider returning nil still produced an entry")
	}

	t.Run("CORS on every response", func(t *testing.T) {
		for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers"} {
			if resp.Header.Get(h) == "" {
				t.Errorf("GET lacks %s", h)
			}
		}
		pre, _ := getCard(t, http.MethodOptions, ts.URL+"/mcp/server-card", "", map[string]string{"Access-Control-Request-Method": "GET"})
		if pre.StatusCode != http.StatusNoContent || pre.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("preflight HTTP %d, ACAO %q", pre.StatusCode, pre.Header.Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("ETag revalidates to 304", func(t *testing.T) {
		etag := resp.Header.Get("ETag")
		if etag == "" || !strings.HasPrefix(resp.Header.Get("Cache-Control"), "public, max-age=") {
			t.Fatalf("ETag %q, Cache-Control %q", etag, resp.Header.Get("Cache-Control"))
		}
		again, body := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", "", map[string]string{"If-None-Match": "W/" + etag})
		if again.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("HTTP %d with %d body bytes, want 304 and none", again.StatusCode, len(body))
		}
		stale, _ := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", "", map[string]string{"If-None-Match": `"stale"`})
		if stale.StatusCode != http.StatusOK {
			t.Fatalf("stale ETag: HTTP %d", stale.StatusCode)
		}
	})

	t.Run("HEAD has headers and no body, POST is refused", func(t *testing.T) {
		head, body := getCard(t, http.MethodHead, ts.URL+"/mcp/server-card", "", nil)
		if head.StatusCode != http.StatusOK || len(body) != 0 || head.Header.Get("ETag") == "" {
			t.Errorf("HEAD HTTP %d, %d body bytes", head.StatusCode, len(body))
		}
		post, _ := getCard(t, http.MethodPost, ts.URL+"/mcp/server-card", "", nil)
		if post.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST HTTP %d", post.StatusCode)
		}
	})

	t.Run("nil provider removes the entry", func(t *testing.T) {
		srv.SetCardExtensionInfo("io.modelcontextprotocol/events", nil)
		_, body := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", "", nil)
		if decodeCard(t, body).Remotes[0].ExtensionsInfo != nil {
			t.Errorf("entry still present: %s", body)
		}
	})
}

// A public Host with no Origin is refused on POST /mcp (#1547), and the card
// is still served, because a host reads it before it can authenticate.
func TestServerCard_OutsideOriginGuardAndAuth(t *testing.T) {
	const host = "mcp.example.com"
	opts := []Option{WithServerCard(testCard...), WithBearerToken("s3cret")}
	if got := originStatus(t, opts, nil, host, "", ""); got != http.StatusForbidden {
		t.Fatalf("POST /mcp under a public Host: HTTP %d, want 403", got)
	}
	srv := NewServer(core.Implementation{Name: "card", Version: "1"}, opts...)
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	resp, body := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", host, map[string]string{"X-Forwarded-Proto": "https"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card under a public Host: HTTP %d: %s", resp.StatusCode, body)
	}
	if got := decodeCard(t, body).Remotes[0].URL; got != "https://"+host+"/mcp" {
		t.Errorf("remote url %q, want the public https URL", got)
	}
}

func TestServerCard_Mounting(t *testing.T) {
	cases := []struct {
		name string
		srv  *Server
		opts []TransportOption
		path string
		want int
	}{
		{"no card configured", NewServer(core.Implementation{Name: "x", Version: "1"}), []TransportOption{WithStreamableHTTP(true)}, "/mcp/server-card", http.StatusNotFound},
		{"SSE only gets no card", cardServer(t), nil, "/mcp/server-card", http.StatusNotFound},
		{"both transports", cardServer(t), []TransportOption{WithStreamableHTTP(true), WithSSE(true)}, "/mcp/server-card", http.StatusOK},
		{"custom prefix", cardServer(t), []TransportOption{WithStreamableHTTP(true), WithPrefix("/v2/mcp")}, "/v2/mcp/server-card", http.StatusOK},
		{"beside WithHTTPHandler", cardServer(t, WithHTTPHandler("GET /healthz", http.NotFoundHandler())), []TransportOption{WithStreamableHTTP(true)}, "/mcp/server-card", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.srv.Handler(tc.opts...))
			defer ts.Close()
			resp, body := getCard(t, http.MethodGet, ts.URL+tc.path, "", nil)
			if resp.StatusCode != tc.want {
				t.Fatalf("HTTP %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

// WithMux mounts the MCP handler at the exact prefix, so the card path needs
// its own entry on that mux.
func TestServerCard_WithMux(t *testing.T) {
	srv := cardServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.RunWithListener(ln, WithStreamableHTTP(true), WithMux(func(m *http.ServeMux) {
		m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {})
	}))
	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("server not ready")
	}
	t.Cleanup(func() { srv.CloseAllSessions(); ln.Close() })
	resp, body := getCard(t, http.MethodGet, "http://"+ln.Addr().String()+"/mcp/server-card", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
}

func TestServerCard_InvalidCardPanicsAtHandler(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []ServerCardOption
	}{
		{"free-form name", []ServerCardOption{WithCardDescription("d")}},
		{"no description", []ServerCardOption{WithCardName("io.github.x/y")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(core.Implementation{Name: "free form", Version: "1"}, WithServerCard(tc.opts...))
			defer func() {
				if recover() == nil {
					t.Fatal("Handler did not panic")
				}
			}()
			srv.Handler(WithStreamableHTTP(true))
		})
	}
}

func TestServerCard_ExplicitRemotes(t *testing.T) {
	srv := NewServer(core.Implementation{Name: "x", Version: "1"}, WithServerCard(append(testCard,
		WithCardRemotes(core.Remote{Type: core.RemoteTypeStreamableHTTP, URL: "https://public.example/mcp"}),
		WithCardRepository(core.Repository{URL: "https://github.com/panyam/mcpkit", Source: "github"}),
		WithCardMaxAge(0),
	)...))
	srv.SetCardExtensionInfo("io.modelcontextprotocol/events", func(remoteURL string) map[string]any {
		return map[string]any{"seen": remoteURL}
	})
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless)))
	t.Cleanup(ts.Close)
	resp, body := getCard(t, http.MethodGet, ts.URL+"/mcp/server-card", "", nil)
	card := decodeCard(t, body)
	r := card.Remotes[0]
	if r.URL != "https://public.example/mcp" || r.ExtensionsInfo["io.modelcontextprotocol/events"]["seen"] != "https://public.example/mcp" {
		t.Errorf("remote %+v", r)
	}
	if strings.Join(r.SupportedProtocolVersions, ",") != strings.Join(core.SupportedStatelessVersions, ",") {
		t.Errorf("stateless-only versions %v", r.SupportedProtocolVersions)
	}
	if card.Repository == nil || card.Repository.Source != "github" {
		t.Errorf("repository %+v", card.Repository)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
}

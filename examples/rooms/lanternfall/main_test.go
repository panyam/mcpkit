package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, closeFn, err := newHandler(log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(func() { ts.Close(); closeFn() })
	return ts
}

func TestJoin_NeedsASignedInPlayerAndAGame(t *testing.T) {
	ts := newTestServer(t)
	res, err := http.Post(ts.URL+"/rooms/join?game=demo", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("join without a player: %d, want 401", res.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if res, err = c.Get(ts.URL + "/signin?as=alice&game=demo"); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res, err = c.Post(ts.URL+"/rooms/join?game=Not%20A%20Game", "", nil); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("join with a bad game: %d, want 403", res.StatusCode)
	}

	if res, err = c.Post(ts.URL+"/rooms/join?game=demo", "", nil); err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var j struct {
		Room           string   `json:"room"`
		RelayURL       string   `json:"relayUrl"`
		BridgeSubjects []string `json:"bridgeSubjects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	if j.Room != "lanternfall:demo" || j.RelayURL != "/relay" || len(j.BridgeSubjects) != 1 || j.BridgeSubjects[0] != bridgeSubject {
		t.Fatalf("join answered %+v", j)
	}
}

type bearer string

func (b bearer) Token() (string, error) { return string(b), nil }

func TestMCP_ListsTheGameToolsAndExplainsTheBridge(t *testing.T) {
	ts := newTestServer(t)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0"}, client.WithTokenSource(bearer("player:alice")))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools {
		have[tl.Name] = true
	}
	for _, want := range []string{"look", "move_player", "reveal", "place_item", "narrate", "attach", "next_request"} {
		if !have[want] {
			t.Errorf("tools/list lacks %s", want)
		}
	}
	if !strings.Contains(c.Instructions, "call attach with the resource") {
		t.Errorf("instructions do not explain attach: %q", c.Instructions)
	}
}

func TestMCP_RefusesAnAgentWithoutAPlayerToken(t *testing.T) {
	ts := newTestServer(t)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0"}, client.WithTokenSource(bearer("someone")))
	if err := c.Connect(context.Background()); err == nil {
		c.Close()
		t.Fatal("connected without a player token")
	}
}

func TestSignin_CookieIsSecureOnlyOverHTTPS(t *testing.T) {
	ts := newTestServer(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		proto  string
		secure bool
	}{{"", false}, {"https", true}} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/signin?as=alice&game=demo", nil)
		if tc.proto != "" {
			req.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		cookies := res.Cookies()
		if len(cookies) != 1 || cookies[0].Name != playerCookie {
			t.Fatalf("proto %q: cookies %v", tc.proto, cookies)
		}
		if cookies[0].Secure != tc.secure {
			t.Errorf("proto %q: Secure=%v, want %v", tc.proto, cookies[0].Secure, tc.secure)
		}
	}
}

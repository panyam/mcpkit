package host

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func TestAuthConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		auth AuthConfig
		env  map[string]string
		want string
	}{
		{"unknown type", AuthConfig{Type: "magic"}, nil, "unknown auth type"},
		{"oauth ok (DCR, no env)", AuthConfig{Type: "oauth"}, nil, ""},
		{"oauth ok (pinned client)", AuthConfig{Type: "oauth", ClientIDEnv: "AC_OA"}, map[string]string{"AC_OA": "cid"}, ""},
		{"oauth pinned env unset", AuthConfig{Type: "oauth", ClientIDEnv: "AC_OA_NOPE"}, nil, "is not set"},
		{"bearer missing env name", AuthConfig{Type: "bearer"}, nil, "requires tokenEnv"},
		{"bearer env unset", AuthConfig{Type: "bearer", TokenEnv: "AGENTCHAT_NO_SUCH"}, nil, "is not set"},
		{"cc missing envs", AuthConfig{Type: "client-credentials"}, nil, "clientIdEnv and clientSecretEnv"},
		{"cc env unset", AuthConfig{Type: "client-credentials", ClientIDEnv: "AC_ID", ClientSecretEnv: "AC_NOPE"},
			map[string]string{"AC_ID": "x"}, "is not set"},
		{"bearer ok", AuthConfig{Type: "bearer", TokenEnv: "AC_TOK"}, map[string]string{"AC_TOK": "t"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := tc.auth.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBearerAuthReachesTheWire(t *testing.T) {
	inner := testutil.NewTestServer().Handler(server.WithStreamableHTTP(true))
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sekrit-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(guarded)
	t.Cleanup(ts.Close)

	t.Setenv("AGENTCHAT_TEST_BEARER", "sekrit-123")
	cfg := testConfig(ts.URL)
	cfg.Servers[0].Auth = &AuthConfig{Type: "bearer", TokenEnv: "AGENTCHAT_TEST_BEARER"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatalf("connect with bearer must succeed: %v", err)
	}
	app.Close()

	// Without the bearer, the guarded server rejects the connection. Under
	// graceful degradation (docs/AGENT_SERVER_STATE.md) this no longer fails
	// boot — the agent starts and the server surfaces as not-ready (401 ->
	// needs-login), which is how we know the un-bearered request was refused.
	cfg.Servers[0].Auth = nil
	app2, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatalf("without bearer, boot should still succeed gracefully: %v", err)
	}
	defer app2.Close()
	if s, _ := app2.group.State(cfg.Servers[0].ID); s == client.StateReady {
		t.Fatalf("un-bearered connection to the guarded server must not be ready, got %v", s)
	}
}

func TestClientCredentialsWiring(t *testing.T) {
	t.Setenv("AC_CC_ID", "svc-client")
	t.Setenv("AC_CC_SECRET", "svc-secret")
	sc := ServerConfig{
		ID:  "svc",
		URL: "https://mcp.example.test/mcp",
		Auth: &AuthConfig{
			Type: "client-credentials", ClientIDEnv: "AC_CC_ID", ClientSecretEnv: "AC_CC_SECRET",
			Scopes: []string{"mcp:basic"}, AllowInsecure: true,
		},
	}
	// The OAuth flow itself is ext/auth's tested territory; agentchat owns
	// only the env-to-field wiring, so inspect exactly that.
	src := clientCredentialsSource(sc)
	if src.ServerURL != sc.URL || src.ClientID != "svc-client" || src.ClientSecret != "svc-secret" {
		t.Fatalf("wiring: %+v", src)
	}
	if len(src.Scopes) != 1 || src.Scopes[0] != "mcp:basic" || !src.AllowInsecure {
		t.Fatalf("wiring: %+v", src)
	}
	if opt, ls, err := authOption(sc); err != nil || opt == nil || ls != nil {
		t.Fatalf("authOption: opt=%v loginSource=%v err=%v", opt, ls, err)
	}
}

func TestAuthOption_OAuthBuildsLoginSource(t *testing.T) {
	t.Setenv("OA_CLIENT", "cid-123")
	sc := ServerConfig{ID: "s", URL: "https://mcp.example.com/mcp", Auth: &AuthConfig{
		Type:        "oauth",
		ClientIDEnv: "OA_CLIENT",
		Scopes:      []string{"mcp:read"},
	}}
	opt, ls, err := authOption(sc)
	if err != nil || opt == nil || ls == nil {
		t.Fatalf("authOption(oauth): opt=%v loginSource=%v err=%v", opt, ls, err)
	}

	// inspect the env-to-field wiring on the concrete source
	src := oauthSource(sc)
	if src.ServerURL != sc.URL || src.ClientID != "cid-123" || src.EnableDCR {
		t.Fatalf("pinned-client wiring: %+v", src)
	}
	// no clientIdEnv → self-register via DCR
	dcr := oauthSource(ServerConfig{URL: sc.URL, Auth: &AuthConfig{Type: "oauth"}})
	if !dcr.EnableDCR || dcr.ClientID != "" {
		t.Fatalf("DCR fallback wiring: %+v", dcr)
	}
}

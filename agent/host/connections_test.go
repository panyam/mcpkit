package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func twoConn() *ConnectionsConfig {
	return &ConnectionsConfig{
		Active: "local",
		Connections: map[string]ConnectionConfig{
			"local": {Type: "lmstudio", Model: "qwen"},
			"cloud": {Type: "openai", Model: "gpt-5", APIKeyEnv: "OPENAI_KEY"},
		},
	}
}

// namedStub is a StubProvider tagged with the connection name it was
// built for, so a test can assert which connection is active.
type namedStub struct {
	*agent.StubProvider
	name string
}

func stubBuilder(t *testing.T) ProviderBuilder {
	// maps model -> connection name for the twoConn fixture
	byModel := map[string]string{"qwen": "local", "gpt-5": "cloud"}
	return func(c ConnectionConfig) (agent.Provider, error) {
		name := byModel[c.Model]
		turns := make([]agent.StubTurn, 8)
		for i := range turns {
			turns[i] = agent.StubTurn{Text: "reply from " + name}
		}
		return &namedStub{StubProvider: agent.NewStubProvider(turns...), name: name}, nil
	}
}

func TestConnectionRegistry_LoadAndActive(t *testing.T) {
	reg, err := NewConnectionRegistry(twoConn(), stubBuilder(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Names(); len(got) != 2 || got[0] != "cloud" || got[1] != "local" {
		t.Fatalf("Names() = %v, want sorted [cloud local]", got)
	}
	if reg.Active() != "local" {
		t.Fatalf("Active() = %q, want local", reg.Active())
	}
	p, err := reg.ActiveProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.(*namedStub).name != "local" {
		t.Fatalf("active provider is %q, want local", p.(*namedStub).name)
	}
}

func TestConnectionRegistry_SetActiveAndCache(t *testing.T) {
	reg, err := NewConnectionRegistry(twoConn(), stubBuilder(t))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := reg.ActiveProvider()

	p, err := reg.SetActive("cloud")
	if err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if p.(*namedStub).name != "cloud" || reg.Active() != "cloud" {
		t.Fatalf("SetActive(cloud) did not switch: active=%q provider=%q", reg.Active(), p.(*namedStub).name)
	}
	// unknown name leaves active unchanged
	if _, err := reg.SetActive("nope"); err == nil {
		t.Fatal("SetActive(unknown) succeeded, want error")
	}
	if reg.Active() != "cloud" {
		t.Fatalf("failed SetActive changed active to %q", reg.Active())
	}
	// switching back returns the SAME cached instance
	back, _ := reg.SetActive("local")
	if back != first {
		t.Fatal("SetActive did not return the cached provider on revisit")
	}
}

func TestConnectionRegistry_ValidationErrors(t *testing.T) {
	cases := map[string]*ConnectionsConfig{
		"no active":        {Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio", Model: "m"}}},
		"active undefined": {Active: "b", Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio", Model: "m"}}},
		"no model":         {Active: "a", Connections: map[string]ConnectionConfig{"a": {Type: "lmstudio"}}},
		"unknown type":     {Active: "a", Connections: map[string]ConnectionConfig{"a": {Type: "bogus", Model: "m"}}},
		"empty":            {Active: "a"},
	}
	for name, cfg := range cases {
		if _, err := NewConnectionRegistry(cfg, stubBuilder(t)); err == nil {
			t.Fatalf("%s: NewConnectionRegistry succeeded, want error", name)
		}
	}
}

func TestResolveBaseURL(t *testing.T) {
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "lmstudio"}); u != "http://localhost:1234/v1" {
		t.Fatalf("lmstudio base = %q", u)
	}
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "lmstudio", BaseURL: "http://x/v1"}); u != "http://x/v1" {
		t.Fatalf("BaseURL should override type, got %q", u)
	}
	if _, err := resolveBaseURL(ConnectionConfig{Model: "m"}); err == nil {
		t.Fatal("no type and no baseUrl should error")
	}
}

// TestConnectionsConfigJSON pins the llm.json-inspired shape decodes.
func TestConnectionsConfigJSON(t *testing.T) {
	raw := `{
      "active": "local",
      "connections": {
        "local": {"type": "lmstudio", "model": "qwen"},
        "think": {"type": "lmstudio", "model": "deepseek",
                  "thinkingHint": {"openTag": "<think>", "closeTag": "</think>"}}
      }
    }`
	var c ConnectionsConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Active != "local" || len(c.Connections) != 2 {
		t.Fatalf("decoded wrong: %+v", c)
	}
	if h := c.Connections["think"].ThinkingHint; h == nil || h.OpenTag != "<think>" {
		t.Fatalf("thinkingHint not decoded: %+v", c.Connections["think"])
	}
}

func TestApp_ProviderSwitchAtRuntime(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Connections = twoConn()

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProviderBuilder(stubBuilder(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	names, active := app.Providers()
	if len(names) != 2 || active != "local" {
		t.Fatalf("Providers() = (%v, %q)", names, active)
	}

	if err := app.SwitchProvider("cloud"); err != nil {
		t.Fatalf("SwitchProvider: %v", err)
	}
	if _, active := app.Providers(); active != "cloud" {
		t.Fatalf("active after switch = %q, want cloud", active)
	}
	// the Runner now streams from the cloud provider
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reply from cloud") {
		t.Fatalf("turn did not use the switched provider:\n%s", out.String())
	}
}

func TestApp_NoConnectionsProvidersEmpty(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if names, active := app.Providers(); names != nil || active != "" {
		t.Fatalf("Providers() without registry = (%v, %q), want empty", names, active)
	}
	if err := app.SwitchProvider("x"); err == nil {
		t.Fatal("SwitchProvider without registry succeeded, want error")
	}
}

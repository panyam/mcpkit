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
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "gemini"}); u != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("gemini base = %q", u)
	}
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "anthropic"}); u != "https://api.anthropic.com" {
		t.Fatalf("anthropic base = %q", u)
	}
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "openrouter"}); u != "https://openrouter.ai/api/v1" {
		t.Fatalf("openrouter base = %q", u)
	}
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "litellm"}); u != "http://localhost:4000/v1" {
		t.Fatalf("litellm base = %q", u)
	}
	// a router connection can override the preset base (hosted litellm)
	if u, _ := resolveBaseURL(ConnectionConfig{Type: "litellm", BaseURL: "https://gw.example/v1"}); u != "https://gw.example/v1" {
		t.Fatalf("litellm BaseURL override = %q", u)
	}
}

// TestThinkingHintWrapsProvider pins that a ThinkingHint on a connection makes
// DefaultProviderBuilder wrap the provider in the inline-reasoning parser, and
// that no hint leaves it unwrapped.
func TestThinkingHintWrapsProvider(t *testing.T) {
	plain, err := DefaultProviderBuilder(ConnectionConfig{Type: "lmstudio", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.(*agent.OpenAIProvider); !ok {
		t.Fatalf("no hint should be the bare provider, got %T", plain)
	}
	hinted, err := DefaultProviderBuilder(ConnectionConfig{
		Type: "lmstudio", Model: "m",
		ThinkingHint: &ThinkingHint{OpenTag: "<think>", CloseTag: "</think>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hinted.(*agent.OpenAIProvider); ok {
		t.Fatal("a ThinkingHint should wrap the provider, but got the bare *agent.OpenAIProvider")
	}
}

// TestRouterPresets_BuildOpenAIWire pins that the router types build the
// OpenAI-wire provider (they are gateways, not a native provider).
func TestRouterPresets_BuildOpenAIWire(t *testing.T) {
	for _, typ := range []string{"openrouter", "litellm"} {
		p, err := DefaultProviderBuilder(ConnectionConfig{Type: typ, Model: "m"})
		if err != nil {
			t.Fatalf("%s build: %v", typ, err)
		}
		if _, ok := p.(*agent.OpenAIProvider); !ok {
			t.Fatalf("%s built %T, want *agent.OpenAIProvider", typ, p)
		}
	}
}

// TestDefaultProviderBuilder_ProviderType pins that only "anthropic" routes to
// the native Messages-API provider; every other type (gemini included) builds
// the OpenAI-wire provider.
func TestDefaultProviderBuilder_ProviderType(t *testing.T) {
	anth, err := DefaultProviderBuilder(ConnectionConfig{Type: "anthropic", Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatalf("anthropic build: %v", err)
	}
	if _, ok := anth.(*agent.AnthropicProvider); !ok {
		t.Fatalf("anthropic type built %T, want *agent.AnthropicProvider", anth)
	}
	gem, err := DefaultProviderBuilder(ConnectionConfig{Type: "gemini", Model: "gemini-2.5-flash"})
	if err != nil {
		t.Fatalf("gemini build: %v", err)
	}
	if _, ok := gem.(*agent.OpenAIProvider); !ok {
		t.Fatalf("gemini type built %T, want *agent.OpenAIProvider", gem)
	}
}

// TestBuildEmbedder pins that openai/gemini connections build an OpenAI-wire
// embedder and anthropic (no embeddings API) is rejected.
func TestBuildEmbedder(t *testing.T) {
	for _, typ := range []string{"openai", "gemini", "lmstudio"} {
		e, err := BuildEmbedder(ConnectionConfig{Type: typ, Model: "embed-model", Dim: 1536}, nil)
		if err != nil {
			t.Fatalf("%s embedder: %v", typ, err)
		}
		if _, ok := e.(*agent.OpenAIEmbedder); !ok {
			t.Fatalf("%s built %T, want *agent.OpenAIEmbedder", typ, e)
		}
	}
	if _, err := BuildEmbedder(ConnectionConfig{Type: "anthropic", Model: "claude"}, nil); err == nil {
		t.Fatal("anthropic embedder should error (no embeddings API)")
	}
}

// TestEmbedderRole covers the ConnectionsConfig.Embedder selector: the lookup
// helper and registry validation of a bad/anthropic embedder name.
func TestEmbedderRole(t *testing.T) {
	cfg := &ConnectionsConfig{
		Active:   "chat",
		Embedder: "emb",
		Connections: map[string]ConnectionConfig{
			"chat": {Type: "openai", Model: "gpt-4o"},
			"emb":  {Type: "openai", Model: "text-embedding-3-small", Dim: 1536},
		},
	}
	conn, ok := cfg.EmbedderConnection()
	if !ok || conn.Model != "text-embedding-3-small" || conn.Dim != 1536 {
		t.Fatalf("EmbedderConnection = %+v, ok=%v", conn, ok)
	}
	if _, err := NewConnectionRegistry(cfg, stubBuilder(t)); err != nil {
		t.Fatalf("valid embedder role should build: %v", err)
	}

	bad := *cfg
	bad.Embedder = "nope"
	if _, err := NewConnectionRegistry(&bad, stubBuilder(t)); err == nil {
		t.Fatal("unknown embedder name should error")
	}

	anth := &ConnectionsConfig{
		Active:   "chat",
		Embedder: "chat",
		Connections: map[string]ConnectionConfig{
			"chat": {Type: "anthropic", Model: "claude-sonnet-5"},
		},
	}
	if _, err := NewConnectionRegistry(anth, stubBuilder(t)); err == nil {
		t.Fatal("anthropic embedder connection should be rejected")
	}
}

// TestConnectionsConfigJSON pins the llm.json-inspired shape decodes.
func TestConnectionsConfigJSON(t *testing.T) {
	raw := `{
      "active": "local",
      "embedder": "emb",
      "connections": {
        "local": {"type": "lmstudio", "model": "qwen"},
        "emb":   {"type": "openai", "model": "text-embedding-3-small", "dim": 1536, "apiKeyEnv": "OPENAI_API_KEY"},
        "think": {"type": "lmstudio", "model": "deepseek",
                  "thinkingHint": {"openTag": "<think>", "closeTag": "</think>"}}
      }
    }`
	var c ConnectionsConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Active != "local" || c.Embedder != "emb" || len(c.Connections) != 3 {
		t.Fatalf("decoded wrong: %+v", c)
	}
	if h := c.Connections["think"].ThinkingHint; h == nil || h.OpenTag != "<think>" {
		t.Fatalf("thinkingHint not decoded: %+v", c.Connections["think"])
	}
	if e := c.Connections["emb"]; e.Dim != 1536 {
		t.Fatalf("dim tag not decoded: %+v", e)
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

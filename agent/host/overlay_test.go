package host

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func TestOverlayPathFor(t *testing.T) {
	cases := map[string]string{
		"kitchen-sink.json": "kitchen-sink.local.json",
		"/a/b/config.json":  "/a/b/config.local.json",
		"noext":             "noext.local",
		"/tmp/x.yaml":       "/tmp/x.local.yaml",
	}
	for in, want := range cases {
		if got := overlayPathFor(in); got != want {
			t.Errorf("overlayPathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

const baseConfigJSON = `{
  "connections": {
    "active": "local",
    "connections": {
      "local": {"type": "lmstudio", "model": "m"},
      "cloud": {"type": "openai", "model": "m", "apiKeyEnv": "K"}
    }
  },
  "servers": [{"id": "demo", "url": "http://localhost:8788/mcp"}],
  "instructions": "base instructions"
}`

// writeBase writes the base config to a temp dir and returns its path.
func writeBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "kitchen-sink.json")
	if err := os.WriteFile(base, []byte(baseConfigJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return base
}

// TestLoadConfigWithOverlay_MergeSemantics pins the contract the whole feature
// rests on: a sparse overlay overrides the scalars it names, preserves the maps
// and slices it omits, and adds nested objects — so a one-line "active" change
// never wipes servers or the connections catalog.
func TestLoadConfigWithOverlay_MergeSemantics(t *testing.T) {
	base := writeBase(t)
	overlay := overlayPathFor(base)
	if err := os.WriteFile(overlay, []byte(`{"connections":{"active":"cloud"},"approval":{"mode":"ask"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigWithOverlay(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections.Active != "cloud" {
		t.Errorf("active = %q, want overlay's cloud", cfg.Connections.Active)
	}
	if len(cfg.Connections.Connections) != 2 {
		t.Errorf("connections map = %d entries, want 2 (base map preserved, not replaced by the sparse overlay)", len(cfg.Connections.Connections))
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].ID != "demo" {
		t.Errorf("servers = %+v, want the base [demo] (overlay omits servers, so they survive)", cfg.Servers)
	}
	if cfg.Approval == nil || approvalModeName(parseApprovalMode(cfg.Approval.Mode)) != "ask" {
		t.Errorf("approval = %+v, want mode ask from the overlay", cfg.Approval)
	}
	if cfg.Instructions != "base instructions" {
		t.Errorf("instructions = %q, want base (overlay omits it)", cfg.Instructions)
	}
}

// TestLoadConfigWithOverlay_SliceReplaceWhenPresent pins the other half of the
// contract: a slice the overlay DOES carry replaces the base slice wholesale
// (json semantics), so this is a deliberate, documented behavior not an accident.
func TestLoadConfigWithOverlay_SliceReplaceWhenPresent(t *testing.T) {
	base := writeBase(t)
	overlay := overlayPathFor(base)
	if err := os.WriteFile(overlay, []byte(`{"servers":[{"id":"other","url":"http://localhost:9999/mcp"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigWithOverlay(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].ID != "other" {
		t.Errorf("servers = %+v, want the overlay's [other] (present slice replaces)", cfg.Servers)
	}
}

func TestLoadConfigWithOverlay_NoOverlayFile(t *testing.T) {
	base := writeBase(t)
	cfg, err := LoadConfigWithOverlay(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections.Active != "local" {
		t.Errorf("active = %q, want base local (no overlay present)", cfg.Connections.Active)
	}
}

// TestConfigOverlay_WriteBackRoundTrip is the end-to-end acceptance: writing a
// pick through the overlay and reloading gets it back, both picks coexist in a
// sparse file, and the base catalog survives.
func TestConfigOverlay_WriteBackRoundTrip(t *testing.T) {
	base := writeBase(t)
	o := &configOverlay{path: overlayPathFor(base)}

	if err := o.setActiveConnection("cloud"); err != nil {
		t.Fatal(err)
	}
	if err := o.setApprovalMode("ask"); err != nil {
		t.Fatal(err)
	}

	// The overlay file holds only the two picks, sparsely.
	raw, err := os.ReadFile(o.path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["connections"].(map[string]any)["active"]; got != "cloud" {
		t.Errorf("overlay connections.active = %v, want cloud", got)
	}
	if got := m["approval"].(map[string]any)["mode"]; got != "ask" {
		t.Errorf("overlay approval.mode = %v, want ask (second write must not clobber the first)", got)
	}
	if _, hasServers := m["servers"]; hasServers {
		t.Error("overlay must stay sparse — it should not contain servers")
	}

	// Reloading merges the picks over the base.
	cfg, err := LoadConfigWithOverlay(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections.Active != "cloud" || len(cfg.Connections.Connections) != 2 {
		t.Errorf("reload: active=%q conns=%d, want cloud with the 2-entry base catalog intact", cfg.Connections.Active, len(cfg.Connections.Connections))
	}
}

// TestDispatchPersistsPicksToOverlay wires the whole path: /provider and
// /approve dispatched through the App write their picks to the overlay when
// WithConfigOverlay is set, so a real slash command (not just the writer)
// persists.
func TestDispatchPersistsPicksToOverlay(t *testing.T) {
	baseDir := t.TempDir()
	basePath := filepath.Join(baseDir, "kitchen-sink.json")
	cfg := &Config{
		Connections: &ConnectionsConfig{
			Active: "local",
			Connections: map[string]ConnectionConfig{
				"local": {Type: "lmstudio", Model: "m"},
				"cloud": {Type: "openai", Model: "m", APIKeyEnv: "K"},
			},
		},
		Approval: &ApprovalConfig{Mode: "allow"},
	}
	build := func(ConnectionConfig) (agent.Provider, error) { return agent.NewStubProvider(), nil }
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""),
		WithProviderBuilder(build), WithConfigOverlay(basePath))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	ctx := context.Background()
	if _, err := app.Dispatch(ctx, "/provider cloud"); err != nil {
		t.Fatalf("/provider cloud: %v", err)
	}
	if _, err := app.Dispatch(ctx, "/approve ask"); err != nil {
		t.Fatalf("/approve ask: %v", err)
	}

	raw, err := os.ReadFile(overlayPathFor(basePath))
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["connections"].(map[string]any)["active"]; got != "cloud" {
		t.Errorf("/provider did not persist: connections.active = %v", got)
	}
	if got := m["approval"].(map[string]any)["mode"]; got != "ask" {
		t.Errorf("/approve did not persist: approval.mode = %v", got)
	}
}

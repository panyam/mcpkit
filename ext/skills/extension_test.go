package skills_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

func TestSkillsExtension_Metadata(t *testing.T) {
	ext := skills.SkillsExtension{}.Extension()
	if ext.ID != skills.ExtensionID {
		t.Errorf("ID = %q, want %q", ext.ID, skills.ExtensionID)
	}
	if ext.SpecVersion == "" {
		t.Errorf("SpecVersion is empty")
	}
	if ext.Stability != core.Experimental {
		t.Errorf("Stability = %q, want experimental", ext.Stability)
	}
}

// TestSkillsExtension_DirectoryReadConfig confirms the SEP-2640
// directoryRead flag (added by commit 2e04c48d on 2026-06-09) emits on
// the wire when set. The bare SkillsExtension{} keeps Config nil so
// servers that wire the extension directly without an attached
// directory handler don't accidentally advertise a method they don't
// actually serve.
func TestSkillsExtension_DirectoryReadConfig(t *testing.T) {
	// Default: no Config.
	defaultExt := skills.SkillsExtension{}.Extension()
	if defaultExt.Config != nil {
		t.Errorf("default SkillsExtension Config = %v, want nil", defaultExt.Config)
	}

	// Opted-in: Config carries directoryRead: true.
	onExt := skills.SkillsExtension{DirectoryRead: true}.Extension()
	v, ok := onExt.Config[skills.CapabilityDirectoryRead].(bool)
	if !ok {
		t.Fatalf("Config[%q] missing or wrong type; Config=%v", skills.CapabilityDirectoryRead, onExt.Config)
	}
	if !v {
		t.Errorf("Config[%q] = false, want true", skills.CapabilityDirectoryRead)
	}
}

func TestSkillsExtension_AppearsInInitialize(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	if !c.ServerSupportsExtension(skills.ExtensionID) {
		t.Errorf("server should advertise %q in capabilities.extensions", skills.ExtensionID)
	}
}

// TestSkillsExtension_AppearsInServerDiscover_ProviderRegisterWith confirms
// the capability surfaces under the SEP-2575 stateless wire (server/discover)
// the same way it does on the legacy initialize wire. Provider.RegisterWith
// is the auto-declaration path; the test asserts the auto-decl carries
// through to server/discover without an extra registration call.
func TestSkillsExtension_AppearsInServerDiscover_ProviderRegisterWith(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "discover-prov", Version: "0.0.1"})
	p, err := skills.NewProvider(skills.WithDirectory("testdata/valid"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)
	assertDiscoverHasSkillsCapability(t, srv)
}

// TestSkillsExtension_AppearsInServerDiscover_WithExtension confirms the
// explicit WithExtension registration path also flows through to
// server/discover. Pairs with the Provider.RegisterWith variant above so
// the dual-wire story is covered for both registration shapes.
func TestSkillsExtension_AppearsInServerDiscover_WithExtension(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "discover-ext", Version: "0.0.1"},
		server.WithExtension(skills.SkillsExtension{}),
	)
	assertDiscoverHasSkillsCapability(t, srv)
}

// assertDiscoverHasSkillsCapability boots the given server in dual-mode
// + stateless-friendly transport, fires server/discover via the public
// Client.Discover() helper, and verifies the skills extension appears
// in the response. Shared by the two registration-path tests.
func assertDiscoverHasSkillsCapability(t *testing.T, srv *server.Server) {
	t.Helper()
	handler := srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithStatelessMode(stateless.ModeDual),
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(
		ts.URL+"/mcp",
		core.ClientInfo{Name: "discover-probe", Version: "0.0.1"},
		client.WithClientMode(client.ClientModeAdaptive),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	dr, err := c.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if dr.Capabilities.Extensions == nil {
		t.Fatalf("Discover response has no capabilities.extensions")
	}
	if _, ok := dr.Capabilities.Extensions[skills.ExtensionID]; !ok {
		t.Errorf("capabilities.extensions missing %q in server/discover; got keys: %v",
			skills.ExtensionID, keysOf(dr.Capabilities.Extensions))
	}
}

func keysOf(m map[string]core.ExtensionCapability) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestClient_Discover_LegacyOnlyServerReturnsTypedError pins down 590's
// acceptance: a server that does not implement server/discover (legacy-
// only mode) makes Client.Discover() return *UnsupportedDiscoverError
// rather than a generic transport-level surprise.
func TestClient_Discover_LegacyOnlyServerReturnsTypedError(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "legacy-only", Version: "0.0.1"})
	handler := srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithStatelessMode(stateless.ModeLegacyOnly),
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(
		ts.URL+"/mcp",
		core.ClientInfo{Name: "discover-probe", Version: "0.0.1"},
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	_, err := c.Discover()
	if err == nil {
		t.Fatal("expected error from legacy-only server, got nil")
	}
	var typed *client.UnsupportedDiscoverError
	if !errors.As(err, &typed) {
		t.Errorf("err = %v (%T), want *client.UnsupportedDiscoverError", err, err)
	}
}

// TestSkillsExtension_JSONShapeIsObject pins down the regression the PHP
// reference impl hit during SEP-2640 review: capabilities.extensions[ID]
// MUST marshal as the empty JSON object {} not the empty array []. The
// SEP example shows {} and hosts that switch on the value's type would
// reject [] as "not an extension config object".
func TestSkillsExtension_JSONShapeIsObject(t *testing.T) {
	caps := core.ServerCapabilities{
		Extensions: map[string]core.ExtensionCapability{
			skills.ExtensionID: {},
		},
	}
	body, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(body)

	// The value for the extension key must be an object literal.
	needle := `"` + skills.ExtensionID + `":{`
	if !strings.Contains(got, needle) {
		t.Errorf("expected substring %q in %s", needle, got)
	}
	// And must not be an array literal.
	wrongNeedle := `"` + skills.ExtensionID + `":[`
	if strings.Contains(got, wrongNeedle) {
		t.Errorf("extension value marshalled as array, want object: %s", got)
	}
}

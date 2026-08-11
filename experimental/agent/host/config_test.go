package host

import (
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

func TestConfigValidate_ConnectionsSupersedeModel(t *testing.T) {
	// connections-only config is valid without a Model block
	c := &Config{
		Connections: &ConnectionsConfig{
			Active:      "local",
			Connections: map[string]ConnectionConfig{"local": {Type: "lmstudio", Model: "m"}},
		},
		Servers: []ServerConfig{{ID: "s", URL: "http://x/mcp"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("connections-only config should validate: %v", err)
	}
	empty := &Config{Servers: []ServerConfig{{ID: "s", URL: "http://x/mcp"}}}
	if err := empty.Validate(); err == nil {
		t.Fatal("config with neither model nor connections should error")
	}
}

func TestParseApprovalMode(t *testing.T) {
	cases := map[string]agent.ApprovalMode{
		"ask":             agent.ModeAlwaysAsk,
		"":                agent.ModeAlwaysAsk,
		"nonsense":        agent.ModeAlwaysAsk,
		"read-only-auto":  agent.ModeReadOnlyAuto,
		"readonly":        agent.ModeReadOnlyAuto,
		"  Read-Only  ":   agent.ModeReadOnlyAuto,
		"reversible-auto": agent.ModeReversibleAuto,
		"reversible":      agent.ModeReversibleAuto,
		// "auto-edit" named the read-only tier before a reversible tier
		// existed, where it never auto-allowed an edit. It points at the
		// mode it always described.
		"auto-edit": agent.ModeReversibleAuto,
		"allow":     agent.ModeAlwaysAllow,
		"yolo":      agent.ModeAlwaysAllow,
	}
	for in, want := range cases {
		if got := parseApprovalMode(in); got != want {
			t.Errorf("parseApprovalMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestApprovalModeNameRoundTrips(t *testing.T) {
	for _, m := range []agent.ApprovalMode{
		agent.ModeAlwaysAsk, agent.ModeReadOnlyAuto, agent.ModeReversibleAuto, agent.ModeAlwaysAllow,
	} {
		if got := parseApprovalMode(approvalModeName(m)); got != m {
			t.Errorf("round-trip of %v via %q = %v", m, approvalModeName(m), got)
		}
	}
}

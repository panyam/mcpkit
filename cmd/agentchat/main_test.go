package main

import (
	"testing"
)

// TestEnvOverridesFlagDefaults pins the viper contract the README documents:
// AGENTCHAT_<FLAG> (dashes as underscores) overrides a flag's default, and an
// explicitly passed flag wins over the env var.
func TestEnvOverridesFlagDefaults(t *testing.T) {
	t.Setenv("AGENTCHAT_MODEL", "env-model")
	t.Setenv("AGENTCHAT_BASE_URL", "http://env:9999/v1")

	root, v := newRoot()
	if err := root.ParseFlags([]string{"--base-url", "http://flag:1111/v1"}); err != nil {
		t.Fatal(err)
	}

	if got := v.GetString("model"); got != "env-model" {
		t.Fatalf("env must override the empty default: model = %q", got)
	}
	if got := v.GetString("base-url"); got != "http://flag:1111/v1" {
		t.Fatalf("explicit flag must beat env: base-url = %q", got)
	}
	if got := v.GetString("instructions"); got == "" {
		t.Fatal("untouched flag must keep its default")
	}
}

// TestBuildConfigRequiresModelOrConfig pins the CLI's quick-start guard
// (moved here from the host config test when App core was extracted).
func TestBuildConfigRequiresModelOrConfig(t *testing.T) {
	if _, err := buildConfig("", nil, "b", "", "", "", 0); err == nil {
		t.Fatal("want error when neither config nor url/model given")
	}
	cfg, err := buildConfig("", []string{"http://x/mcp"}, "b", "m", "", "sys", 0)
	if err != nil || len(cfg.Servers) != 1 || cfg.Model.Model != "m" {
		t.Fatalf("valid quick-start config: %+v %v", cfg, err)
	}
}

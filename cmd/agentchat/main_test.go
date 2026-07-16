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

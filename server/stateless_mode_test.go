package server

import (
	"testing"
)

func TestStatelessModeString(t *testing.T) {
	cases := []struct {
		mode StatelessMode
		want string
	}{
		{StatelessModeLegacyOnly, "legacy"},
		{StatelessModeDual, "dual"},
		{StatelessModeStateless, "stateless"},
		{StatelessMode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("StatelessMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestParseStatelessMode(t *testing.T) {
	cases := []struct {
		in     string
		want   StatelessMode
		wantOK bool
	}{
		{"legacy", StatelessModeLegacyOnly, true},
		{"dual", StatelessModeDual, true},
		{"stateless", StatelessModeStateless, true},
		{"LEGACY", StatelessModeLegacyOnly, true},
		{"  Dual  ", StatelessModeDual, true},
		{"", StatelessModeDual, false},
		{"nonsense", StatelessModeDual, false},
	}
	for _, c := range cases {
		got, ok := ParseStatelessMode(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseStatelessMode(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if ok && got != c.want {
			t.Errorf("ParseStatelessMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveStatelessMode_EnvBeatsDefault(t *testing.T) {
	t.Setenv(statelessModeEnvVar, "legacy")
	defaultStatelessModeMu.Lock()
	prev := DefaultStatelessMode
	DefaultStatelessMode = StatelessModeStateless
	defaultStatelessModeMu.Unlock()
	t.Cleanup(func() {
		defaultStatelessModeMu.Lock()
		DefaultStatelessMode = prev
		defaultStatelessModeMu.Unlock()
	})

	got := resolveStatelessMode()
	if got != StatelessModeLegacyOnly {
		t.Errorf("resolveStatelessMode with env=legacy returned %v, want %v",
			got, StatelessModeLegacyOnly)
	}
}

func TestResolveStatelessMode_DefaultWhenEnvUnsetOrInvalid(t *testing.T) {
	t.Setenv(statelessModeEnvVar, "garbage")
	defaultStatelessModeMu.Lock()
	prev := DefaultStatelessMode
	DefaultStatelessMode = StatelessModeStateless
	defaultStatelessModeMu.Unlock()
	t.Cleanup(func() {
		defaultStatelessModeMu.Lock()
		DefaultStatelessMode = prev
		defaultStatelessModeMu.Unlock()
	})

	got := resolveStatelessMode()
	if got != StatelessModeStateless {
		t.Errorf("resolveStatelessMode with invalid env returned %v, want %v",
			got, StatelessModeStateless)
	}
}

func TestResolveStatelessMode_BuildDefaultIsDual(t *testing.T) {
	// The shipping default: a fresh build with no overrides yields Dual.
	t.Setenv(statelessModeEnvVar, "")
	got := resolveStatelessMode()
	if got != StatelessModeDual {
		t.Errorf("shipping-default resolveStatelessMode = %v, want %v (Dual)",
			got, StatelessModeDual)
	}
}

func TestWithStatelessMode_OverridesSeed(t *testing.T) {
	// Simulate the seed → option-application flow that happens inside
	// ListenAndServe / Handler.
	cfg := defaultTransportConfig()
	originalSeed := cfg.statelessMode

	WithStatelessMode(StatelessModeStateless)(&cfg)
	if cfg.statelessMode != StatelessModeStateless {
		t.Errorf("after WithStatelessMode(Stateless): got %v, want %v",
			cfg.statelessMode, StatelessModeStateless)
	}
	if originalSeed == StatelessModeStateless {
		t.Skip("seed already matched the override; can't distinguish — env-dependent test setup")
	}
}

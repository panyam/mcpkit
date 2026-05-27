package stateless

import (
	"testing"
)

func TestModeString(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeLegacyOnly, "legacy"},
		{ModeDual, "dual"},
		{ModeStateless, "stateless"},
		{Mode(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		in     string
		want   Mode
		wantOK bool
	}{
		{"legacy", ModeLegacyOnly, true},
		{"dual", ModeDual, true},
		{"stateless", ModeStateless, true},
		{"LEGACY", ModeLegacyOnly, true},
		{"  Dual  ", ModeDual, true},
		{"", ModeDual, false},
		{"nonsense", ModeDual, false},
	}
	for _, c := range cases {
		got, ok := ParseMode(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseMode(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if ok && got != c.want {
			t.Errorf("ParseMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveMode_EnvBeatsDefault(t *testing.T) {
	t.Setenv(ModeEnvVar, "legacy")
	prev := DefaultMode
	SetDefaultMode(ModeStateless)
	t.Cleanup(func() { SetDefaultMode(prev) })

	if got := ResolveMode(); got != ModeLegacyOnly {
		t.Errorf("ResolveMode with env=legacy returned %v, want %v",
			got, ModeLegacyOnly)
	}
}

func TestResolveMode_DefaultWhenEnvUnsetOrInvalid(t *testing.T) {
	t.Setenv(ModeEnvVar, "garbage")
	prev := DefaultMode
	SetDefaultMode(ModeStateless)
	t.Cleanup(func() { SetDefaultMode(prev) })

	if got := ResolveMode(); got != ModeStateless {
		t.Errorf("ResolveMode with invalid env returned %v, want %v",
			got, ModeStateless)
	}
}

func TestResolveMode_BuildDefaultIsDual(t *testing.T) {
	// The shipping default: a fresh build with no overrides yields Dual.
	t.Setenv(ModeEnvVar, "")
	if got := ResolveMode(); got != ModeDual {
		t.Errorf("shipping-default ResolveMode = %v, want %v (Dual)",
			got, ModeDual)
	}
}

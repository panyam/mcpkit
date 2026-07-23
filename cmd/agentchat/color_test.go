package main

import "testing"

// TestResolveColorEnabled pins the E2 precedence: --no-color wins, then
// NO_COLOR (present at any value), then TERM=dumb, else color is on.
func TestResolveColorEnabled(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	cases := []struct {
		name string
		flag bool
		vars map[string]string
		want bool
	}{
		{"clean env is color-on", false, nil, true},
		{"flag disables", true, nil, false},
		{"flag beats a colorful env", true, map[string]string{"COLORTERM": "truecolor"}, false},
		{"NO_COLOR present disables", false, map[string]string{"NO_COLOR": "1"}, false},
		{"NO_COLOR empty still disables", false, map[string]string{"NO_COLOR": ""}, false},
		{"TERM=dumb disables", false, map[string]string{"TERM": "dumb"}, false},
		{"TERM=xterm keeps color", false, map[string]string{"TERM": "xterm-256color"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveColorEnabled(c.flag, env(c.vars)); got != c.want {
				t.Fatalf("resolveColorEnabled(%v, %v) = %v, want %v", c.flag, c.vars, got, c.want)
			}
		})
	}
}

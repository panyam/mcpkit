package auth

import "testing"

func TestTenantOf(t *testing.T) {
	cases := []struct {
		principal string
		want      string
	}{
		{"tenant-a/alice", "tenant-a"},
		{"alice", ""},
		{"", ""},
		{"/alice", ""},
		{"a/b/c", "a"},
		{"/", ""},
	}
	for _, tc := range cases {
		if got := TenantOf(tc.principal); got != tc.want {
			t.Errorf("TenantOf(%q) = %q, want %q", tc.principal, got, tc.want)
		}
	}
}

func TestSubjectOf(t *testing.T) {
	cases := []struct {
		principal string
		want      string
	}{
		{"tenant-a/alice", "alice"},
		{"alice", "alice"},
		{"", ""},
		{"/alice", "alice"},
		{"a/b/c", "b/c"},
		{"/", ""},
	}
	for _, tc := range cases {
		if got := SubjectOf(tc.principal); got != tc.want {
			t.Errorf("SubjectOf(%q) = %q, want %q", tc.principal, got, tc.want)
		}
	}
}

package core

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

func TestPrincipalFor(t *testing.T) {
	cases := []struct {
		name   string
		claims *Claims
		want   string
	}{
		{"nil claims → empty principal", nil, ""},
		{"tenant + subject", &Claims{Subject: "alice", Tenant: "tenant-a"}, "tenant-a/alice"},
		{"subject only, no tenant", &Claims{Subject: "alice"}, "alice"},
		{"empty subject is still a valid principal value", &Claims{Tenant: "tenant-a"}, "tenant-a/"},
		{"empty claims", &Claims{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrincipalFor(tc.claims); got != tc.want {
				t.Errorf("PrincipalFor(%+v) = %q, want %q", tc.claims, got, tc.want)
			}
		})
	}
}

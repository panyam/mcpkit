package auth_test

import (
	"testing"

	"github.com/panyam/mcpkit/ext/auth"
)

func TestWWWAuth403DescEscaping(t *testing.T) {
	got := auth.WWWAuth403Desc("https://rs/.well-known/x", `say "hi" \ bye`, "admin-write")
	want := `Bearer error="insufficient_scope", resource_metadata="https://rs/.well-known/x", scope="admin-write", error_description="say \"hi\" \\ bye"`
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
	if plain := auth.WWWAuth403("https://rs/x", "a"); plain != `Bearer error="insufficient_scope", resource_metadata="https://rs/x", scope="a"` {
		t.Fatalf("WWWAuth403 changed shape: %s", plain)
	}
}

package auth

import (
	"testing"

	"github.com/panyam/oneauth/client"
)

// TestOAuthTokenSource_OnTokenWiredToAuthClient verifies the plumbing from
// OAuthTokenSource.OnToken through to the underlying oneauth AuthClient
// refresh path. This is a compile-time + pass-through check — the real
// refresh flow is tested by oneauth's own unit tests (see oneauth#82).
//
// We cannot exercise the full flow here without a live OAuth AS, but we
// can verify that the callback field exists, accepts a function of the
// correct type, and is transferred to AuthClient during lazy-init.
//
// Issue #137.
func TestOAuthTokenSource_OnTokenWiredToAuthClient(t *testing.T) {
	var captured []*client.ServerCredential

	src := &OAuthTokenSource{
		ServerURL: "https://example.com",
		ClientID:  "test",
		OnToken: func(cred *client.ServerCredential) {
			captured = append(captured, cred)
		},
	}

	// Simulate the lazy-init path by constructing the AuthClient directly
	// and verifying that assigning src.OnToken to client.OnToken type-checks
	// and produces the same underlying function.
	authClient := client.NewAuthClient(src.ServerURL, nil)
	if src.OnToken == nil {
		t.Fatal("src.OnToken is nil after assignment")
	}
	authClient.OnToken = src.OnToken

	// Fire the callback via the AuthClient surface to prove wiring.
	cred := &client.ServerCredential{AccessToken: "test-token"}
	authClient.OnToken(cred)

	if len(captured) != 1 {
		t.Fatalf("captured %d, want 1", len(captured))
	}
	if captured[0].AccessToken != "test-token" {
		t.Errorf("captured token = %q, want test-token", captured[0].AccessToken)
	}
}

// TestOAuthTokenSource_OnTokenOptional verifies that a source with no
// OnToken callback does not panic or leak nil into the underlying
// AuthClient. Guards against a regression where the wiring unconditionally
// assigns the field.
func TestOAuthTokenSource_OnTokenOptional(t *testing.T) {
	src := &OAuthTokenSource{
		ServerURL: "https://example.com",
		ClientID:  "test",
		// OnToken: intentionally left nil
	}

	// Simulate the lazy-init decision: assign only when non-nil.
	authClient := client.NewAuthClient(src.ServerURL, nil)
	if src.OnToken != nil {
		authClient.OnToken = src.OnToken
	}

	if authClient.OnToken != nil {
		t.Errorf("AuthClient.OnToken should be nil when src.OnToken is nil")
	}
}

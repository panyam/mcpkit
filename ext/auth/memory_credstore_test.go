package auth

import (
	"testing"
	"time"

	"github.com/panyam/oneauth/client"
)

// TestMemoryCredentialStore_RoundTrip verifies that SetCredential followed
// by GetCredential returns an equivalent credential for the same URL.
// Baseline sanity check.
func TestMemoryCredentialStore_RoundTrip(t *testing.T) {
	store := NewMemoryCredentialStore()
	cred := &client.ServerCredential{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if err := store.SetCredential("https://example.com", cred); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.GetCredential("https://example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil credential")
	}
	if got.AccessToken != "at-1" || got.RefreshToken != "rt-1" {
		t.Errorf("got = %+v, want at-1/rt-1", got)
	}
}

// TestMemoryCredentialStore_GetReturnsCopy verifies that GetCredential
// returns a copy, not a pointer aliased with internal state. Mutating the
// returned credential must not affect subsequent reads — otherwise a
// careless caller can corrupt the store.
func TestMemoryCredentialStore_GetReturnsCopy(t *testing.T) {
	store := NewMemoryCredentialStore()
	cred := &client.ServerCredential{AccessToken: "at-1"}
	store.SetCredential("https://example.com", cred)

	first, _ := store.GetCredential("https://example.com")
	first.AccessToken = "hijacked"

	second, _ := store.GetCredential("https://example.com")
	if second.AccessToken != "at-1" {
		t.Errorf("second read = %q, want at-1 (store was mutated through returned pointer)", second.AccessToken)
	}
}

// TestMemoryCredentialStore_SetStoresCopy verifies that SetCredential
// stores a copy of the caller's credential. Mutating the credential after
// Set must not affect future reads.
func TestMemoryCredentialStore_SetStoresCopy(t *testing.T) {
	store := NewMemoryCredentialStore()
	cred := &client.ServerCredential{AccessToken: "at-1"}
	store.SetCredential("https://example.com", cred)
	cred.AccessToken = "hijacked"

	got, _ := store.GetCredential("https://example.com")
	if got.AccessToken != "at-1" {
		t.Errorf("stored = %q, want at-1 (store retained caller's mutable pointer)", got.AccessToken)
	}
}

// TestMemoryCredentialStore_MissingKey verifies that GetCredential returns
// (nil, nil) for an unknown server URL. Matches the CredentialStore
// interface contract used elsewhere in oneauth.
func TestMemoryCredentialStore_MissingKey(t *testing.T) {
	store := NewMemoryCredentialStore()
	got, err := store.GetCredential("https://nowhere.example")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// TestMemoryCredentialStore_Remove verifies that RemoveCredential deletes
// the entry and subsequent Get returns nil.
func TestMemoryCredentialStore_Remove(t *testing.T) {
	store := NewMemoryCredentialStore()
	store.SetCredential("https://example.com", &client.ServerCredential{AccessToken: "at-1"})
	if err := store.RemoveCredential("https://example.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ := store.GetCredential("https://example.com")
	if got != nil {
		t.Errorf("got = %+v after Remove, want nil", got)
	}
}

// TestMemoryCredentialStore_ListServers verifies that ListServers returns
// all stored URLs.
func TestMemoryCredentialStore_ListServers(t *testing.T) {
	store := NewMemoryCredentialStore()
	store.SetCredential("https://a.example", &client.ServerCredential{AccessToken: "a"})
	store.SetCredential("https://b.example", &client.ServerCredential{AccessToken: "b"})
	servers, _ := store.ListServers()
	if len(servers) != 2 {
		t.Errorf("got %d servers, want 2", len(servers))
	}
}

// TestMemoryCredentialStore_ZeroValueIsUsable verifies that the zero
// value of MemoryCredentialStore (no NewMemoryCredentialStore call) is
// safe to use — Set initializes the backing map lazily. Defensive for
// callers that embed the store in a struct.
func TestMemoryCredentialStore_ZeroValueIsUsable(t *testing.T) {
	var store MemoryCredentialStore
	if err := store.SetCredential("https://a.example", &client.ServerCredential{AccessToken: "a"}); err != nil {
		t.Fatalf("Set on zero value: %v", err)
	}
	got, _ := store.GetCredential("https://a.example")
	if got == nil || got.AccessToken != "a" {
		t.Errorf("got = %+v, want AccessToken=a", got)
	}
}

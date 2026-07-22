package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/panyam/oneauth/client"
)

func TestResolveClientID_CIMDGate(t *testing.T) {
	var registrations []ClientRegistrationRequest
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var meta ClientRegistrationRequest
		json.NewDecoder(r.Body).Decode(&meta)
		registrations = append(registrations, meta)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client-id"})
	}))
	t.Cleanup(reg.Close)

	const cimdURL = "https://conformance-test.local/client-metadata.json"
	mkSource := func(advertised, enableDCR bool, regEndpoint string) *OAuthTokenSource {
		return &OAuthTokenSource{
			ClientMetadataURL: cimdURL,
			EnableDCR:         enableDCR,
			authInfo: &MCPAuthInfo{
				AuthorizationServers: []string{"https://as.example"},
				ASMetadata: &client.ASMetadata{
					Issuer:                            "https://as.example",
					TokenEndpoint:                     "https://as.example/token",
					RegistrationEndpoint:              regEndpoint,
					ClientIdMetadataDocumentSupported: advertised,
				},
			},
		}
	}

	t.Run("advertised uses metadata URL", func(t *testing.T) {
		id, _, err := mkSource(true, true, reg.URL).resolveClientID()
		if err != nil {
			t.Fatalf("resolveClientID: %v", err)
		}
		if id != cimdURL {
			t.Errorf("client_id = %q, want the CIMD URL", id)
		}
	})

	t.Run("not advertised with DCR available falls back to DCR", func(t *testing.T) {
		before := len(registrations)
		id, _, err := mkSource(false, true, reg.URL).resolveClientID()
		if err != nil {
			t.Fatalf("resolveClientID: %v", err)
		}
		if id != "dcr-client-id" {
			t.Errorf("client_id = %q, want DCR-issued id", id)
		}
		if len(registrations) != before+1 {
			t.Errorf("expected one DCR registration, got %d", len(registrations)-before)
		}
	})

	t.Run("not advertised without DCR keeps metadata URL", func(t *testing.T) {
		id, _, err := mkSource(false, false, "").resolveClientID()
		if err != nil {
			t.Fatalf("resolveClientID: %v", err)
		}
		if id != cimdURL {
			t.Errorf("client_id = %q, want the CIMD URL (no fallback available)", id)
		}
	})
}

func TestDefaultClientRegistration_IncludesRefreshTokenGrant(t *testing.T) {
	meta := DefaultClientRegistration()
	if !slices.Contains(meta.GrantTypes, "authorization_code") {
		t.Error("grant_types missing authorization_code")
	}
	if !slices.Contains(meta.GrantTypes, "refresh_token") {
		t.Error("grant_types missing refresh_token (SEP-2207)")
	}
}

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterClient_Success verifies that RegisterClient correctly POSTs a
// DCR request and parses the response containing client_id and client_secret.
func TestRegisterClient_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", http.StatusBadRequest)
			return
		}

		var req ClientRegistrationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Verify the request payload
		if req.ClientName == "" {
			http.Error(w, "missing client_name", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ClientRegistrationResponse{
			ClientID:     "registered-client-123",
			ClientSecret: "secret-456",
		})
	}))
	t.Cleanup(srv.Close)

	resp, err := RegisterClient(srv.URL, DefaultClientRegistration(), srv.Client())
	if err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}
	if resp.ClientID != "registered-client-123" {
		t.Errorf("client_id = %q, want %q", resp.ClientID, "registered-client-123")
	}
	if resp.ClientSecret != "secret-456" {
		t.Errorf("client_secret = %q, want %q", resp.ClientSecret, "secret-456")
	}
}

// TestRegisterClient_ServerError verifies that RegisterClient returns an error
// when the DCR endpoint responds with a non-success status.
func TestRegisterClient_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "registration disabled", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := RegisterClient(srv.URL, DefaultClientRegistration(), srv.Client())
	if err == nil {
		t.Fatal("expected error on 403 response")
	}
}

// TestRegisterClient_EmptyClientID verifies that RegisterClient returns an error
// when the DCR endpoint returns a response without a client_id.
func TestRegisterClient_EmptyClientID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{})
	}))
	t.Cleanup(srv.Close)

	_, err := RegisterClient(srv.URL, DefaultClientRegistration(), srv.Client())
	if err == nil {
		t.Fatal("expected error when client_id is empty")
	}
}

// TestDefaultClientRegistration verifies that the default DCR request has
// sensible values for an MCP client.
func TestDefaultClientRegistration(t *testing.T) {
	meta := DefaultClientRegistration()
	if meta.ClientName == "" {
		t.Error("missing client_name")
	}
	if len(meta.RedirectURIs) == 0 {
		t.Error("missing redirect_uris")
	}
	if len(meta.GrantTypes) == 0 {
		t.Error("missing grant_types")
	}
}

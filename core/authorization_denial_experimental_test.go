package core

import (
	"encoding/json"
	"testing"
)

// TestAuthorizationDenialSerialization verifies that the AuthorizationDenial
// envelope round-trips correctly through JSON, including nested remediation hints.
func TestAuthorizationDenialSerialization(t *testing.T) {
	denial := AuthorizationDenial{
		Reason:                 "insufficient_authorization",
		AuthorizationContextID: "authzctx_abc123",
		RemediationHints: []RemediationHint{
			ScopeStepUpHint([]string{"read", "write"}),
		},
	}

	data, err := json.Marshal(denial)
	if err != nil {
		t.Fatal(err)
	}

	var got AuthorizationDenial
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Reason != "insufficient_authorization" {
		t.Errorf("Reason = %q, want %q", got.Reason, "insufficient_authorization")
	}
	if got.AuthorizationContextID != "authzctx_abc123" {
		t.Errorf("AuthorizationContextID = %q, want %q", got.AuthorizationContextID, "authzctx_abc123")
	}
	if len(got.RemediationHints) != 1 {
		t.Fatalf("RemediationHints len = %d, want 1", len(got.RemediationHints))
	}
	hint := got.RemediationHints[0]
	if hint.Type != RemediationTypeOAuthScopeStepUp {
		t.Errorf("hint.Type = %q, want %q", hint.Type, RemediationTypeOAuthScopeStepUp)
	}
	scopes, ok := hint.Extra["requiredScopes"]
	if !ok {
		t.Fatal("missing requiredScopes in hint")
	}
	scopeList, ok := scopes.([]any)
	if !ok || len(scopeList) != 2 {
		t.Errorf("requiredScopes = %v, want [read write]", scopes)
	}
}

// TestRemediationHintFlatWireFormat verifies that hint members appear at the
// top level of the JSON object (per SEP-2643), not nested under a `data` field.
func TestRemediationHintFlatWireFormat(t *testing.T) {
	hint := OAuthAuthorizationDetailsHint([]any{
		map[string]any{"type": "payment_initiation"},
	})

	data, err := json.Marshal(hint)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["data"]; ok {
		t.Error("hint must not nest members under 'data' (SEP-2643 wire format)")
	}
	if _, ok := raw["authorization_details"]; !ok {
		t.Error("authorization_details must appear at top level")
	}
	if _, ok := raw["type"]; !ok {
		t.Error("type must be present")
	}
}

// TestAuthorizationDenialOmitsEmptyFields verifies that optional fields
// (authorizationContextId, credential_disposition, remediationHints) are
// omitted from JSON when not set.
func TestAuthorizationDenialOmitsEmptyFields(t *testing.T) {
	denial := AuthorizationDenial{
		Reason: "insufficient_authorization",
	}

	data, err := json.Marshal(denial)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	if _, ok := raw["authorizationContextId"]; ok {
		t.Error("authorizationContextId should be omitted when empty")
	}
	if _, ok := raw["credentialDisposition"]; ok {
		t.Error("credentialDisposition should be omitted when empty")
	}
	if _, ok := raw["remediationHints"]; ok {
		t.Error("remediationHints should be omitted when empty")
	}
}

// TestNewAuthorizationDenialError verifies the error response constructor
// produces the expected JSON-RPC error structure with the authorization
// denial envelope nested in error.data.
func TestNewAuthorizationDenialError(t *testing.T) {
	denial := AuthorizationDenial{
		Reason:                 "insufficient_authorization",
		AuthorizationContextID: "authzctx_test",
		RemediationHints: []RemediationHint{
			ScopeStepUpHint([]string{"write"}),
		},
	}

	resp := NewAuthorizationDenialError(
		json.RawMessage(`1`),
		ErrCodeToolExecutionError,
		"write scope required",
		denial,
	)

	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodeToolExecutionError {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeToolExecutionError)
	}

	// Verify data contains authorization envelope.
	dataBytes, err := json.Marshal(resp.Error.Data)
	if err != nil {
		t.Fatal(err)
	}
	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(dataBytes, &dataMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := dataMap["authorization"]; !ok {
		t.Fatal("error data missing 'authorization' key")
	}
}

// TestScopeStepUpHint verifies the convenience constructor produces the
// expected hint structure with requiredScopes in data.
func TestScopeStepUpHint(t *testing.T) {
	hint := ScopeStepUpHint([]string{"read", "write", "admin"})
	if hint.Type != RemediationTypeOAuthScopeStepUp {
		t.Errorf("Type = %q, want %q", hint.Type, RemediationTypeOAuthScopeStepUp)
	}
	scopes, ok := hint.Extra["requiredScopes"].([]string)
	if !ok {
		t.Fatal("requiredScopes not []string")
	}
	if len(scopes) != 3 {
		t.Errorf("requiredScopes len = %d, want 3", len(scopes))
	}
}

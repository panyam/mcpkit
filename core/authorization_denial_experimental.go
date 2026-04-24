package core

// EXPERIMENTAL: Authorization denial types for the FineGrainedAuth proposal (draft SEP).
//
// These types enable structured authorization denial responses at the JSON-RPC
// layer, complementing transport-level challenges (HTTP WWW-Authenticate).
// The denial envelope classifies the failure and may carry remediation hints
// that augment what the transport can express on its own.
//
// Type names, field names, error codes, and wire format are subject to
// breaking changes as the SEP evolves. Do not depend on stability.
//
// See: FineGrainedAuth.docx for the full design document.

// Provisional remediation hint type constants.
// EXPERIMENTAL: these names will change when the SEP assigns final values.
const (
	// RemediationTypeOAuthScopeStepUp indicates the client should re-authorize
	// with broader OAuth scopes. The hint Data carries RequiredScopes.
	RemediationTypeOAuthScopeStepUp = "oauth_scope_step_up"

	// RemediationTypeOAuthRAR indicates the client should re-authorize with
	// RFC 9396 Rich Authorization Request authorization_details.
	// PLACEHOLDER: waiting on oneauth RAR support. Do not use yet.
	RemediationTypeOAuthRAR = "oauth_authorization_details"
)

// AuthorizationDenial is the structured authorization denial envelope
// returned in JSON-RPC error data. It classifies the failure and optionally
// carries remediation hints.
//
// EXPERIMENTAL: subject to rename/removal as the FineGrainedAuth SEP evolves.
type AuthorizationDenial struct {
	// Reason classifies the denial (e.g., "insufficient_authorization").
	// EXPERIMENTAL: values will be standardized when the SEP assigns them.
	Reason string `json:"reason"`

	// AuthorizationContextID is an optional correlation handle. When provided,
	// the client SHOULD echo it on retry in _meta. Servers should not require
	// it when the credential presented on retry is sufficient on its own.
	AuthorizationContextID string `json:"authorizationContextId,omitempty"`

	// CredentialDisposition signals the lifecycle relationship between a
	// newly-issued credential and existing credentials.
	//   - "replacement" (default): new credential supersedes existing
	//   - "additional": new credential coexists with existing (UC3/PSD2)
	//
	// EXPERIMENTAL: UC3 semantics not yet implemented. Placeholder for
	// future oneauth RAR integration.
	CredentialDisposition string `json:"credential_disposition,omitempty"`

	// RemediationHints are optional MCP-level hints that complement the
	// transport's authorization challenge. Each hint has a type that
	// determines its schema.
	RemediationHints []RemediationHint `json:"remediationHints,omitempty"`
}

// RemediationHint is a single remediation suggestion within an authorization
// denial. The Type field determines what Data contains.
//
// EXPERIMENTAL: subject to rename/removal as the FineGrainedAuth SEP evolves.
type RemediationHint struct {
	// Type identifies the remediation mechanism (e.g., "oauth_scope_step_up",
	// "oauth_authorization_details", "identity_assertion_grant").
	Type string `json:"type"`

	// Data carries type-specific remediation parameters. The schema depends
	// on Type. For oauth_scope_step_up, this contains RequiredScopes.
	// For oauth_authorization_details (future), this contains the RFC 9396 array.
	Data map[string]any `json:"data,omitempty"`
}

// ScopeStepUpHint creates a RemediationHint for OAuth scope step-up (UC2).
// The requiredScopes list tells the client which scopes to request on re-authorization.
func ScopeStepUpHint(requiredScopes []string) RemediationHint {
	return RemediationHint{
		Type: RemediationTypeOAuthScopeStepUp,
		Data: map[string]any{
			"requiredScopes": requiredScopes,
		},
	}
}

// NewAuthorizationDenialError creates a JSON-RPC error response with an
// authorization denial envelope in the error data. The error code should
// be chosen based on the use case:
//   - UC1 (URL approval): use ErrCodeURLElicitationRequired (-32042)
//   - UC2/UC3 (credential change): use the tool execution error code
//
// The denial is composed into the error data alongside any other fields
// (e.g., elicitations for UC1).
//
// EXPERIMENTAL: subject to change as the FineGrainedAuth SEP evolves.
func NewAuthorizationDenialError(id []byte, code int, message string, denial AuthorizationDenial) *Response {
	return NewErrorResponseWithData(id, code, message, map[string]any{
		"authorization": denial,
	})
}

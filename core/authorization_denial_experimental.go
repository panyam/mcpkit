package core

// EXPERIMENTAL: Authorization denial types for SEP-2643 (Structured Authorization
// Denials). Tracks the draft SEP at:
//
//	https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2643
//
// These types enable structured authorization denial responses at the JSON-RPC
// layer, complementing transport-level challenges (HTTP WWW-Authenticate).
// The denial envelope classifies the failure and may carry remediation hints
// that augment what the transport can express on its own.
//
// Type names, field names, error codes, and wire format may change as the
// SEP evolves. Open spec items tracked:
//   - JSON-RPC error code value (`<AUTHORIZATION_DENIAL_TBD>`) — Open Question 1
//
// SEP-defined hint types (this SEP defines two):
//   - "url" — composes with URL-mode elicitation (UC1)
//   - "oauth_authorization_details" — RFC 9396 RAR remediation (UC2 example 2, UC3)

import (
	"encoding/json"
)

// SEP-defined remediation hint type values.
const (
	// RemediationTypeURL indicates remediation via URL-mode elicitation.
	// Composes with the existing URLElicitationRequiredError (-32042).
	RemediationTypeURL = "url"

	// RemediationTypeOAuthRAR indicates the client should re-authorize with
	// RFC 9396 Rich Authorization Request authorization_details. The hint
	// carries an "authorization_details" field at the top level.
	RemediationTypeOAuthRAR = "oauth_authorization_details"

	// RemediationTypeOAuthScopeStepUp is a NON-SPEC extension used to carry
	// requiredScopes from server to client at the JSON-RPC layer. SEP-2643
	// says scope step-up is fully described by the WWW-Authenticate challenge
	// and the envelope should carry only classification metadata. Tracked in
	// issue #317 — to be removed once ext/auth supports declarative per-tool
	// scopes + 403/WWW-Authenticate scope-enforcement middleware.
	RemediationTypeOAuthScopeStepUp = "oauth_scope_step_up"
)

// Retry meta key per SEP-2643. The client MUST echo the server-issued
// authorizationContextId verbatim under this key on retry.
const MetaKeyAuthorizationContextID = "io.modelcontextprotocol/authorization-context-id"

// AuthorizationDenial is the structured authorization denial envelope
// returned in JSON-RPC error data. It classifies the failure and optionally
// carries remediation hints.
//
// EXPERIMENTAL: subject to rename/removal as SEP-2643 evolves.
type AuthorizationDenial struct {
	// Reason classifies the denial. The SEP currently defines a single value:
	// "insufficient_authorization". Additional values may be defined in future SEPs.
	Reason string `json:"reason"`

	// AuthorizationContextID is an optional correlation handle. When provided,
	// the client MUST echo it on retry in _meta under MetaKeyAuthorizationContextID.
	// Per spec, servers MUST NOT reject a retry solely because the handle is
	// absent or cannot be resolved.
	AuthorizationContextID string `json:"authorizationContextId,omitempty"`

	// CredentialDisposition signals the lifecycle relationship between a
	// newly-issued credential and existing credentials.
	//   - "replacement" (default): new credential supersedes existing
	//   - "additional": new credential coexists with existing (UC3)
	CredentialDisposition string `json:"credentialDisposition,omitempty"`

	// RemediationHints are optional MCP-level hints that complement the
	// transport's authorization challenge. Each hint has a type that
	// determines its additional members (which appear at the top level of
	// the hint object, NOT nested under a `data` field).
	RemediationHints []RemediationHint `json:"remediationHints,omitempty"`
}

// RemediationHint is a single remediation suggestion within an authorization
// denial. Per SEP-2643, each hint has a `type` field plus zero or more
// type-specific members at the **top level** of the object (not nested).
//
// To match the SEP wire format, this type uses custom JSON marshaling that
// flattens Extra fields onto the top level alongside `type`.
//
// Example wire format for the oauth_authorization_details hint:
//
//	{
//	  "type": "oauth_authorization_details",
//	  "authorization_details": [ ... ]
//	}
//
// EXPERIMENTAL: subject to rename/removal as SEP-2643 evolves.
type RemediationHint struct {
	// Type identifies the remediation mechanism. SEP-defined values:
	// "url", "oauth_authorization_details".
	Type string

	// Extra carries type-specific members. These are flattened to the
	// top level of the hint object on marshal (per SEP wire format).
	Extra map[string]any
}

// MarshalJSON flattens Extra at the top level of the JSON object, alongside
// the "type" field, per SEP-2643.
func (h RemediationHint) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(h.Extra)+1)
	for k, v := range h.Extra {
		m[k] = v
	}
	m["type"] = h.Type
	return json.Marshal(m)
}

// UnmarshalJSON splits the "type" field from the other top-level members,
// reversing the flattening done by MarshalJSON.
func (h *RemediationHint) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if t, ok := m["type"].(string); ok {
		h.Type = t
	}
	delete(m, "type")
	if len(m) > 0 {
		h.Extra = m
	}
	return nil
}

// URLHint creates a SEP-2643 remediation hint of type "url".
// Used for UC1 (URL-based approval composed with URLElicitationRequiredError).
func URLHint() RemediationHint {
	return RemediationHint{Type: RemediationTypeURL}
}

// OAuthAuthorizationDetailsHint creates a SEP-2643 remediation hint of type
// "oauth_authorization_details" carrying an RFC 9396 authorization_details array.
// Used for UC2 example 2 (RAR) and UC3 (additional credential).
func OAuthAuthorizationDetailsHint(authorizationDetails any) RemediationHint {
	return RemediationHint{
		Type: RemediationTypeOAuthRAR,
		Extra: map[string]any{
			"authorization_details": authorizationDetails,
		},
	}
}

// ScopeStepUpHint creates a NON-SPEC remediation hint carrying requiredScopes.
// SEP-2643 doesn't define this hint type — the spec position is that scope
// step-up is fully described by the WWW-Authenticate challenge. We keep this
// helper for the fine-grained-auth example until our auth middleware is
// updated to emit proper 403 + WWW-Authenticate responses.
func ScopeStepUpHint(requiredScopes []string) RemediationHint {
	return RemediationHint{
		Type: RemediationTypeOAuthScopeStepUp,
		Extra: map[string]any{
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

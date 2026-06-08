package core

import "strings"

// PrincipalTenantSeparator is the single character that separates the
// tenant prefix from the subject inside an encoded principal string.
// Validators in ext/auth populate Claims.Subject and Claims.Tenant as
// separate typed fields; consumers that need a string-form identity
// (session binding, webhook canonical-key, audit logs) call PrincipalFor
// to get the encoded form. The encoding is the canonical contract.
//
// Pinned to "/" because it is unambiguous in OAuth subject identifiers
// (subjects are opaque strings; an embedded "/" would already break any
// tenancy-naive equality check upstream) and matches the natural
// Keycloak realm-URL slash layout. Changing the separator is a wire-
// shape change — bump a major version, do not flip it in a patch.
const PrincipalTenantSeparator = "/"

// PrincipalFor returns the string-form principal for an authenticated
// Claims value — Tenant + PrincipalTenantSeparator + Subject when
// Tenant is non-empty, otherwise Subject verbatim. This is the
// canonical encoder; every consumer that needs string-form identity
// (session binding, webhook canonical-key, audit logs) should call
// PrincipalFor rather than concatenate Tenant and Subject by hand.
// Returns "" for a nil Claims, matching ClaimsProvider's "no claims"
// convention.
//
// Lives in core/ (not ext/auth) because the encoding is a property of
// Claims itself — consumers like experimental/ext/events and server's
// session-binding need it without taking an auth-extension dependency.
func PrincipalFor(claims *Claims) string {
	if claims == nil {
		return ""
	}
	if claims.Tenant == "" {
		return claims.Subject
	}
	return claims.Tenant + PrincipalTenantSeparator + claims.Subject
}

// TenantOf returns the tenant prefix of a previously-encoded principal
// string, or "" when the string carries no tenant prefix. Useful when
// a consumer holds a principal string (e.g., the principal stored on
// a webhook target) rather than a live Claims value — within a request
// path, prefer reading Claims.Tenant directly.
//
// Examples:
//
//	TenantOf("tenant-a/alice")    == "tenant-a"
//	TenantOf("alice")             == ""        // single-tenant deployment
//	TenantOf("")                  == ""
//	TenantOf("/alice")            == ""        // leading separator → empty tenant
//	TenantOf("a/b/c")             == "a"       // splits on first separator only
func TenantOf(principal string) string {
	i := strings.IndexByte(principal, PrincipalTenantSeparator[0])
	if i <= 0 {
		return ""
	}
	return principal[:i]
}

// SubjectOf returns the subject portion of a previously-encoded
// principal string — everything after the first PrincipalTenantSeparator.
// When the string has no separator (single-tenant deployment),
// SubjectOf returns the input verbatim. Pair with TenantOf when a
// caller needs both halves from a stored principal; live requests
// should read Claims.Subject directly.
//
// Examples:
//
//	SubjectOf("tenant-a/alice")   == "alice"
//	SubjectOf("alice")            == "alice"   // no separator → no tenant
//	SubjectOf("")                 == ""
//	SubjectOf("/alice")           == "alice"   // leading separator → empty tenant
//	SubjectOf("a/b/c")            == "b/c"     // splits on first separator only
func SubjectOf(principal string) string {
	i := strings.IndexByte(principal, PrincipalTenantSeparator[0])
	if i < 0 {
		return principal
	}
	return principal[i+1:]
}

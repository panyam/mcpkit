package auth

import (
	"errors"
	"fmt"
)

// ErrIssMismatch is returned by the OAuth callback when the RFC 9207
// `iss` query parameter is present in the authorization response but
// does not match the issuer identifier of the authorization server the
// client sent the user to.
//
// This is the OAuth-callback analogue of an issuer-mismatch attack
// (RFC 9207 Mix-Up Protection): an attacker substitutes a different
// AS's redirect with a code that LOOKS valid (matching state, correct
// shape) but `iss` carries the attacker's AS identifier. Without this
// check the client would proceed to token exchange against the wrong
// AS and accept a token minted by an unintended issuer.
//
// Wrapped with diagnostic context (PRM issuer, observed iss) so
// callers logging the error can see what was rejected; consumers
// branching on the failure mode should use errors.Is.
var ErrIssMismatch = errors.New("RFC 9207 iss does not match authorization server issuer")

// ErrIssMissing is returned when the authorization server advertised
// `authorization_response_iss_parameter_supported: true` in its AS
// metadata but the authorization response omitted the `iss` query
// parameter. RFC 9207 §2.4 says clients MUST treat the parameter as
// REQUIRED when the AS advertises support — silently accepting the
// omission would degrade the mix-up protection the advertisement
// promised.
//
// Distinct from ErrIssMismatch so audit / log consumers can tell
// "advertised but absent" apart from "present but wrong" — different
// failure modes, different remediation.
var ErrIssMissing = errors.New("RFC 9207 iss missing despite authorization server advertising support")

// validateIss applies the RFC 9207 §2.4 client-side validation rule
// against an `iss` query parameter received on an OAuth callback.
// Wired into OAuthTokenSource via the oneauth BrowserLoginRequest
// OnCallback hook (since oneauth#235 surfaces but does not enforce);
// kept package-private because the only consumer today is the token
// source's callback closure.
//
// Rules (RFC 9207 §2.4 default mode, strict-mode tracked separately
// via panyam/oneauth#238):
//
//	iss present + matches expected  → nil
//	iss present + mismatches        → ErrIssMismatch
//	iss absent + AS advertised      → ErrIssMissing
//	iss absent + AS did not advertise → nil (legacy AS, pre-9207)
//
// Comparison is byte-for-byte. RFC 9207 §2.4 explicitly forbids
// normalization (case folding, default-port elision, trailing-slash,
// percent-encoding) — the upstream `sep-2468-client-no-normalization`
// check enforces this. Distinct from PRM resource validation
// (discovery.go), which DOES normalize because RFC 8707 has different
// semantics; conflating the two rules would defeat the mix-up
// protection the byte-strict comparison is designed to provide.
func validateIss(iss, expectedIssuer string, asAdvertisedSupport bool) error {
	if iss == "" {
		if asAdvertisedSupport {
			return fmt.Errorf("%w: expected=%q", ErrIssMissing, expectedIssuer)
		}
		return nil
	}
	if iss != expectedIssuer {
		return fmt.Errorf("%w: got=%q expected=%q", ErrIssMismatch, iss, expectedIssuer)
	}
	return nil
}

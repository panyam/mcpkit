package events

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// webhookSecretPrefix is the spec-mandated prefix for client-supplied
// webhook signing secrets, matching the convention used by Stripe and
// the Standard Webhooks specification.
const webhookSecretPrefix = "whsec_"

// minSecretBytes / maxSecretBytes are the spec-mandated bounds on the
// raw-byte length of the secret (after base64-decoding the value
// following the whsec_ prefix). 24 bytes = 192 bits floor; 64 bytes =
// 512 bits ceiling.
const (
	minSecretBytes = 24
	maxSecretBytes = 64
)

// GenerateSecret returns a high-entropy "whsec_<base64>" string suitable
// for use as a webhook signing secret. 32 random bytes → 256 bits, well
// inside the spec-mandated 24-64 byte range. Exported wrapper used by
// the Go SDK + demo tests so the spec-conformant format isn't hard-coded
// in multiple places.
func GenerateSecret() string {
	return generateSecret()
}

// generateSecret is the package-private implementation of GenerateSecret.
// Kept private so the secret_test.go round-trip tests don't have to
// reference the exported name.
func generateSecret() string {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Should never fail on supported platforms; fall back to a
		// recognizably weak placeholder so the failure is visible
		// rather than silent.
		return "whsec_INSECURE_FALLBACK"
	}
	return webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(buf[:])
}

// validateClientSecret enforces the spec's webhook-secret format on a
// client-supplied delivery.secret. The value MUST be `whsec_` followed
// by a base64-encoded payload that decodes to between 24 and 64 raw
// bytes (per the Standard Webhooks profile the events spec adopts).
//
// Returns nil on success, otherwise a descriptive error suitable for
// use as the message of a -32602 InvalidParams response. Servers MUST
// reject malformed secrets at subscribe time rather than letting a
// subscription exist that can never produce verifiable deliveries.
//
// Both standard base64 (RFC 4648 §4) and raw URL-safe base64 (§5) are
// accepted on input — the SDKs don't agree on which they emit.
func validateClientSecret(s string) error {
	if !strings.HasPrefix(s, webhookSecretPrefix) {
		return errors.New("must start with the whsec_ prefix")
	}
	body := s[len(webhookSecretPrefix):]
	if body == "" {
		return errors.New("missing the random portion after whsec_")
	}

	// Try standard base64 first (with padding), then raw URL-safe (no
	// padding) — covers both forms in common use.
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(body)
		if err != nil {
			return errors.New("portion after whsec_ must be base64-encoded")
		}
	}

	if len(decoded) < minSecretBytes {
		return errors.New("must decode to at least 24 random bytes (192 bits)")
	}
	if len(decoded) > maxSecretBytes {
		return errors.New("must decode to at most 64 random bytes (512 bits)")
	}
	return nil
}

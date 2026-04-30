package events

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// WebhookSecretMode selects how the registry decides on the per-subscription
// HMAC secret at events/subscribe time. Defaults to WebhookSecretServer:
// the server always generates a fresh high-entropy secret regardless of
// what the client supplies.
type WebhookSecretMode int

const (
	// WebhookSecretServer (default): server always generates a fresh
	// high-entropy secret and returns it. Client-supplied delivery.secret
	// is ignored. Casey's preferred default — entropy quality assurance,
	// avoids the server having to validate client-side entropy.
	WebhookSecretServer WebhookSecretMode = iota

	// WebhookSecretClient: client provides delivery.secret. If empty, the
	// server falls back to generating one. Useful for deployments that
	// pre-provision secrets (e.g., via IaC) where the gateway has the
	// secret before the subscribe call.
	WebhookSecretClient

	// WebhookSecretIdentity: secret is derived deterministically from a
	// canonical tuple of (url, name, params) using HMAC-SHA256 against a
	// server-side root. Subscribe is idempotent on the tuple — same
	// inputs produce the same secret and the same id, so a re-subscribe
	// with identical params returns the existing subscription rather
	// than creating a duplicate. id is also derived (hash of tuple).
	// Requires WithWebhookRoot at construction; otherwise registration
	// errors. Per Peter Alexander's writeup on PR#1 line 391 (~30%
	// confidence at time of writing).
	WebhookSecretIdentity
)

// String renders the mode as a config-flag-friendly token.
func (m WebhookSecretMode) String() string {
	switch m {
	case WebhookSecretClient:
		return "client"
	case WebhookSecretIdentity:
		return "identity"
	default:
		return "server"
	}
}

// ParseSecretMode converts a flag-style token to a WebhookSecretMode.
// Empty string returns the default (WebhookSecretServer).
func ParseSecretMode(s string) (WebhookSecretMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "server":
		return WebhookSecretServer, nil
	case "client":
		return WebhookSecretClient, nil
	case "identity":
		return WebhookSecretIdentity, nil
	default:
		return WebhookSecretServer, fmt.Errorf("unknown secret mode %q (want server|client|identity)", s)
	}
}

// generateSecret returns a high-entropy "whsec_<base64>" string suitable
// for use as a webhook signing secret. 32 random bytes → 256 bits.
func generateSecret() string {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Should never fail on supported platforms; fall back to a
		// recognizably weak placeholder so the failure is visible
		// rather than silent.
		return "whsec_INSECURE_FALLBACK"
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(buf[:])
}

// canonicalTuple produces the input bytes for identity-mode HMAC derivation.
// The wire-stable shape is: name + 0x1f + url + 0x1f + canonical(params).
// 0x1f (US, unit separator) avoids collisions from any printable character.
//
// params is canonicalized by sorting keys lexicographically and serializing
// as key=value pairs joined by 0x1e (RS, record separator). Nil/empty
// params is permitted and serializes to the empty string.
func canonicalTuple(name, url string, params map[string]string) []byte {
	const (
		us = "\x1f" // unit separator between fields
		rs = "\x1e" // record separator between params
	)
	var paramStr string
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + params[k]
		}
		paramStr = strings.Join(parts, rs)
	}
	return []byte(name + us + url + us + paramStr)
}

// deriveIdentitySecret computes the HMAC-SHA256(root, canonicalTuple) and
// returns it as a recognizable "whsec_<hex>" string. Stable across calls
// for the same inputs.
func deriveIdentitySecret(root []byte, name, url string, params map[string]string) string {
	mac := hmac.New(sha256.New, root)
	mac.Write(canonicalTuple(name, url, params))
	return "whsec_" + hex.EncodeToString(mac.Sum(nil))
}

// deriveIdentityID computes a stable subscription id from the same tuple,
// suitable as a routing key for receivers. Returned as "sub_<base64>" using
// raw URL-safe base64 with no padding.
func deriveIdentityID(name, url string, params map[string]string) string {
	sum := sha256.Sum256(canonicalTuple(name, url, params))
	return "sub_" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

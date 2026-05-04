package events

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// canonicalKey returns the canonical-bytes encoding of a webhook subscription
// identity tuple per spec §"Subscription Identity" → "Key composition" L363:
//
//	(principal, delivery.url, name, params)
//
// The four components are joined by 0x1f (US, unit separator); params is
// canonicalized by lexicographically sorting keys and JSON-encoding the
// resulting object so map iteration order doesn't affect the bytes.
//
// Two subscribe calls produce identical canonical bytes if and only if all
// four components match — which is the spec's idempotency rule (§"Subscription
// Identity" → "Key composition" L363: "There is no client-generated id —
// a subscription is fully determined by what it listens for, where it
// delivers, and who asked").
//
// principal is read from the authenticated session's claims.Subject by the
// subscribe handler. For unauthenticated demos, the events.Config field
// UnsafeAnonymousPrincipal stands in for the missing claims.Subject; this
// is an explicit deviation from the spec's "MUST reject unauthenticated"
// rule (L361) and is gated by the Unsafe-prefixed name + a startup warning
// log on the server.
func canonicalKey(principal, deliveryURL, name string, params map[string]any) []byte {
	var paramBytes []byte
	if len(params) == 0 {
		paramBytes = []byte("{}")
	} else {
		paramBytes = canonicalJSON(params)
	}
	// 0x1f (Unit Separator) is non-printable and won't collide with any
	// reasonable principal/url/name/params content. Joining with a
	// printable character (e.g. ":") would let a crafted principal
	// containing the separator merge into the next field.
	const sep = "\x1f"
	var b strings.Builder
	b.Grow(len(principal) + len(deliveryURL) + len(name) + len(paramBytes) + 3)
	b.WriteString(principal)
	b.WriteString(sep)
	b.WriteString(deliveryURL)
	b.WriteString(sep)
	b.WriteString(name)
	b.WriteString(sep)
	b.Write(paramBytes)
	return []byte(b.String())
}

// canonicalJSON serializes a map[string]any with sorted top-level keys so
// the byte output is stable regardless of map iteration order. Nested
// maps and slices are JSON-marshaled with their natural ordering — this
// is sufficient for the spec's "params is compared by canonical-JSON
// equality" requirement (§"Subscription Identity" → "Key composition"
// L363) because subscription params are flat key-value by convention;
// servers that accept nested params should document the canonicalization
// they apply.
func canonicalJSON(m map[string]any) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kJSON, _ := json.Marshal(k)
		vJSON, _ := json.Marshal(m[k])
		b.Write(kJSON)
		b.WriteByte(':')
		b.Write(vJSON)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

// deriveSubscriptionID returns a routing handle of the form
// "sub_<base64-of-16-bytes>" derived from the canonical tuple bytes via
// SHA-256 truncated to 128 bits. Surfaced on the wire as the
// X-MCP-Subscription-Id header on every webhook delivery POST per
// spec §"Webhook Event Delivery" L390 and §"Webhook Security" → "Signature
// scheme" L472.
//
// Collision analysis:
//
//   - Birthday bound: ~2^64 subscriptions before 50% accidental collision.
//     At 1T subs forever, P(collision) ≈ 5.4e-15 — effectively zero.
//   - Engineered collision (find any colliding pair): ~2^64 hash evals,
//     marginally feasible for a nation-state attacker over weeks/months.
//   - Targeted preimage (collide with a specific victim tuple): ~2^128 work,
//     infeasible for anyone.
//
// The id is intentionally non-load-bearing for security per spec
// §"Subscription Identity" → "Cross-tenant isolation" L378: "A caller who
// learns another tenant's derived id gains nothing — id is not accepted
// as input to any method." Even a successful collision lets an attacker
// only cause routing weirdness on the receiver side, where HMAC
// verification with the wrong secret fails by construction.
//
// Truncation can be widened to 24 or 32 bytes if a deployment wants
// extra headroom — change the slice bound below; no API change required.
func deriveSubscriptionID(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sub_" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

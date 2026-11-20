package events

import (
	"crypto"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/panyam/oneauth/utils"
	webhooks "github.com/panyam/servicekit/http/webhooks"
)

// Server identity (SEP-3415 §"Webhook security", "Server identity
// (optional)"): besides the per-subscription v1 HMAC, a server may sign every
// delivery with an ed25519 key and publish the public half on its Server
// Card. A gateway that trusts the card can then tell which server sent a
// delivery, which the shared HMAC secret can't prove.
//
// The SEP names Standard Webhooks v1a. Standard Webhooks deprecated v1a for
// v2a on 2026-11-18 (standard-webhooks PR 372), and the events WG hasn't
// moved the SEP yet, so deliveries carry v1a. servicekit's webhooks package
// already signs v2a for when it does (#1437).

// SigningKey is one ed25519 key the server signs deliveries with.
type SigningKey struct {
	// KID names the key in the published JWK set. Empty means the RFC 8037
	// thumbprint of the public key.
	KID string

	// Signer holds the private key. An ed25519.PrivateKey works, and so does
	// a KMS or HSM signer whose Public() returns an ed25519.PublicKey. v1a
	// hands the signer the whole delivery content, so a KMS that caps the
	// message size fails on large bodies.
	Signer crypto.Signer
}

// SigningJWKAlg is the JWK "alg" for published signing keys (RFC 8037 §3.1).
const SigningJWKAlg = "EdDSA"

// WithWebhookSigningKeys signs every delivery, control envelope and
// verification POST with each key as a v1a signature, after the v1 HMAC.
// Passing more than one key is how rotation works: publish the new key
// beside the old one, sign with both, and drop the old key once gateways
// have refetched the card.
//
// It panics when a key's Public() is not an ed25519.PublicKey or when two
// keys share a KID, since either is a configuration error that would
// otherwise publish keys nobody can verify against.
//
// Under MCPHeaders mode no v1a signature is sent, because that wire format
// has no webhook-signature header. NewWebhookRegistry logs a warning then.
func WithWebhookSigningKeys(keys ...SigningKey) WebhookOption {
	resolved := make([]resolvedSigningKey, 0, len(keys))
	seen := map[string]bool{}
	for i, k := range keys {
		if k.Signer == nil {
			panic(fmt.Sprintf("events: WithWebhookSigningKeys: key %d has no Signer", i))
		}
		pub, ok := k.Signer.Public().(ed25519.PublicKey)
		if !ok {
			panic(fmt.Sprintf("events: WithWebhookSigningKeys: key %d is %T, want ed25519", i, k.Signer.Public()))
		}
		kid := k.KID
		if kid == "" {
			var err error
			if kid, err = utils.ComputeKid(pub, SigningJWKAlg); err != nil {
				panic(fmt.Sprintf("events: WithWebhookSigningKeys: key %d: %v", i, err))
			}
		}
		if seen[kid] {
			panic(fmt.Sprintf("events: WithWebhookSigningKeys: duplicate kid %q", kid))
		}
		seen[kid] = true
		resolved = append(resolved, resolvedSigningKey{kid: kid, signer: k.Signer, public: pub})
	}
	return func(r *WebhookRegistry) {
		r.signingKeys = resolved
	}
}

type resolvedSigningKey struct {
	kid    string
	signer crypto.Signer
	public ed25519.PublicKey
}

// SigningJWKS returns the public halves of the registry's signing keys as a
// JWK set: kty OKP, crv Ed25519, alg EdDSA, use sig. Publish it on the
// Server Card at remotes[].extensionsInfo["io.modelcontextprotocol/events"]
// .webhooks.signingKeys. With no signing keys it returns an empty set.
func (r *WebhookRegistry) SigningJWKS() utils.JWKSet {
	set := utils.JWKSet{Keys: make([]utils.JWK, 0, len(r.signingKeys))}
	for _, k := range r.signingKeys {
		set.Keys = append(set.Keys, utils.Ed25519PublicKeyToJWK(k.kid, SigningJWKAlg, k.public))
	}
	return set
}

// sign builds one signed delivery in the registry's header mode. In
// StandardWebhooks mode each signing key adds a v1a entry after the v1 HMAC.
// A signer that fails is logged and left out, so a KMS outage costs the
// server-identity signature and not the delivery.
func (r *WebhookRegistry) sign(msgID string, body []byte, secret string, now time.Time) signedDelivery {
	d := signFor(r.headerMode, msgID, body, secret, now)
	if r.headerMode != StandardWebhooks || len(r.signingKeys) == 0 {
		return d
	}
	ts := d.headers[webhooks.HeaderTimestamp]
	sig := d.headers[webhooks.HeaderSignature]
	for _, k := range r.signingKeys {
		v1a, err := webhooks.SignV1a(k.signer, msgID, ts, body)
		if err != nil {
			r.logf("[webhook] v1a signing with kid %q failed: %v", k.kid, err)
			continue
		}
		sig += " " + v1a
	}
	d.headers[webhooks.HeaderSignature] = sig
	return d
}

func (r *WebhookRegistry) warnIfSigningKeysUnused() {
	if len(r.signingKeys) > 0 && r.headerMode != StandardWebhooks {
		r.logf("[webhook] WithWebhookSigningKeys has no effect in %s header mode: v1a needs the Standard Webhooks webhook-signature header", r.headerMode)
	}
}

// VerifyServerIdentity reports which key in set signed a delivery. msgID,
// timestamp and signature are the webhook-id, webhook-timestamp and
// webhook-signature header values. It checks each v1a entry against each
// Ed25519 key in set and returns the kid of the first match. Keys of other
// types are skipped.
//
// set must come from the server's Server Card, fetched from the same origin
// the gateway connects to for MCP, never from the delivery itself (SEP-3415
// §"Webhook security"). Like the v1 check, this proves nothing about
// freshness: check TimestampWithinTolerance and dedup on msgID too.
func VerifyServerIdentity(set utils.JWKSet, msgID, timestamp string, body []byte, signature string) (kid string, ok bool) {
	for _, jwk := range set.Keys {
		if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
			continue
		}
		pub, _, err := utils.JWKToPublicKey(jwk)
		if err != nil {
			continue
		}
		if webhooks.VerifyV1a([]ed25519.PublicKey{pub.(ed25519.PublicKey)}, msgID, timestamp, body, signature) {
			return jwk.Kid, true
		}
	}
	return "", false
}

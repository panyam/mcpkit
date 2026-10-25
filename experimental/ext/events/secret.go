package events

import webhooks "github.com/panyam/servicekit/http/webhooks"

// webhookSecretPrefix is the spec-mandated prefix for client-supplied
// webhook signing secrets, matching the convention used by Stripe and
// the Standard Webhooks specification.
const webhookSecretPrefix = webhooks.SecretPrefix

// GenerateSecret returns a high-entropy "whsec_<base64>" string suitable
// for use as a webhook signing secret: 32 random bytes as standard padded
// base64, the alphabet the Standard Webhooks verifier libraries decode
// (#1574). It wraps servicekit's webhooks.GenerateSecret (#1578).
func GenerateSecret() string {
	return webhooks.GenerateSecret()
}

// generateSecret is the package-private name the tests use.
func generateSecret() string {
	return webhooks.GenerateSecret()
}

// validateClientSecret enforces the spec's webhook-secret format on a
// client-supplied delivery.secret: `whsec_` followed by standard or raw
// URL-safe base64 that decodes to 24 to 64 bytes. The error's message is
// written for a -32602 InvalidParams response. Servers MUST reject malformed
// secrets at subscribe time rather than letting a subscription exist that can
// never produce verifiable deliveries.
func validateClientSecret(s string) error {
	return webhooks.ValidateSecret(s)
}

package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestGenerateSecret_PrefixAndEntropy verifies generated secrets have the
// expected wire-recognizable prefix and meaningful entropy (distinct on
// repeated calls). Used by client SDKs to auto-fill delivery.secret when
// the application doesn't supply one.
func TestGenerateSecret_PrefixAndEntropy(t *testing.T) {
	a := generateSecret()
	b := generateSecret()
	assert.True(t, strings.HasPrefix(a, "whsec_"), "must start with whsec_")
	assert.True(t, strings.HasPrefix(b, "whsec_"))
	assert.NotEqual(t, a, b, "two generated secrets must differ")
	assert.Greater(t, len(a), 30, "secret should carry meaningful entropy")
}

// Standard Webhooks verifiers decode the part after whsec_ with standard
// padded base64 (the Go reference library uses base64.StdEncoding), so a
// URL-safe or unpadded secret is unreadable to them (#1574).
func TestGenerateSecret_DecodesAsStandardBase64(t *testing.T) {
	for i := 0; i < 64; i++ {
		s := GenerateSecret()
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "whsec_"))
		if !assert.NoError(t, err, "secret %q must be standard padded base64", s) {
			return
		}
		assert.Len(t, raw, 32)
		mac := hmac.New(sha256.New, raw)
		mac.Write([]byte("evt_1.1700000000.{}"))
		assert.Equal(t, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)),
			signStandardWebhooks("evt_1", []byte("{}"), s, time.Unix(1700000000, 0)).headers["webhook-signature"],
			"the key mcpkit signs with must match what a Standard Webhooks verifier derives")
		assert.NoError(t, validateClientSecret(s))
	}
}

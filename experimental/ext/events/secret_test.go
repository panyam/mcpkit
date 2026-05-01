package events

import (
	"strings"
	"testing"

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

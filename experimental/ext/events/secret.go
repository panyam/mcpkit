package events

import (
	"crypto/rand"
	"encoding/base64"
)

// generateSecret returns a high-entropy "whsec_<base64>" string suitable
// for use as a webhook signing secret. 32 random bytes → 256 bits, well
// inside the spec-mandated 24-64 byte range. Client SDKs use this to
// auto-generate when the application doesn't supply a secret of its own.
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

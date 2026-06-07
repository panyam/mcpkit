package events

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withCapturingLogger replaces the registry's logger with a capture
// function and returns the captured lines after the test. Tests inject
// this via the package-internal logf field rather than going through a
// public option, so we can pin the warning shape without exposing
// logger-injection as a supported API.
func withCapturingLogger(r *WebhookRegistry) func() []string {
	var mu sync.Mutex
	var lines []string
	r.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, format)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(lines))
		copy(out, lines)
		return out
	}
}

func TestWebhookTTL_DefaultIsOneHour(t *testing.T) {
	r := NewWebhookRegistry()
	assert.Equal(t, time.Hour, r.ttl, "default TTL must be DefaultWebhookTTL = 1h")
	assert.Equal(t, time.Hour, DefaultWebhookTTL)
}

func TestWebhookTTL_InEnvelopeNoClamp(t *testing.T) {
	// Construct an empty registry, attach the capturing logger BEFORE
	// applying options so the constructor's logf field stays set to our
	// capture function.
	r := &WebhookRegistry{
		ttl:              DefaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
	}
	getLines := withCapturingLogger(r)
	WithWebhookTTL(30 * time.Minute)(r)
	r.applyTTLClamp()
	assert.Equal(t, 30*time.Minute, r.ttl, "in-envelope TTL must be honored verbatim")
	for _, line := range getLines() {
		assert.NotContains(t, line, "WARNING", "no warning should fire for in-envelope TTL")
	}
}

func TestWebhookTTL_ClampLowToMinimum(t *testing.T) {
	r := &WebhookRegistry{
		ttl:              DefaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
	}
	getLines := withCapturingLogger(r)
	WithWebhookTTL(30 * time.Second)(r)
	r.applyTTLClamp()
	assert.Equal(t, MinWebhookTTL, r.ttl, "sub-floor TTL must clamp UP to MinWebhookTTL (5m)")
	require.NotEmpty(t, getLines(), "clamp must log a warning")
	require.True(t, anyLineContains(getLines(), "below the spec envelope floor"),
		"warning must explain the floor was breached; got %v", getLines())
}

func TestWebhookTTL_ClampHighToMaximum(t *testing.T) {
	r := &WebhookRegistry{
		ttl:              DefaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
	}
	getLines := withCapturingLogger(r)
	WithWebhookTTL(48 * time.Hour)(r)
	r.applyTTLClamp()
	assert.Equal(t, MaxWebhookTTL, r.ttl, "above-ceiling TTL must clamp DOWN to MaxWebhookTTL (24h)")
	require.NotEmpty(t, getLines(), "clamp must log a warning")
	require.True(t, anyLineContains(getLines(), "above the spec envelope ceiling"),
		"warning must explain the ceiling was breached; got %v", getLines())
}

func TestWebhookTTL_UnsafeBypassDisablesClamp(t *testing.T) {
	r := &WebhookRegistry{
		ttl:              DefaultWebhookTTL,
		headerMode:       StandardWebhooks,
		maxBodyBytes:     defaultWebhookMaxBodyBytes,
		suspendThreshold: defaultWebhookSuspendThreshold,
		suspendWindow:    defaultWebhookSuspendWindow,
	}
	getLines := withCapturingLogger(r)
	WithUnsafeWebhookTTLBypass()(r)
	WithWebhookTTL(2 * time.Second)(r)
	r.applyTTLClamp()
	assert.Equal(t, 2*time.Second, r.ttl, "bypass must honor any positive TTL verbatim")
	require.True(t, anyLineContains(getLines(), "WithUnsafeWebhookTTLBypass"),
		"bypass must log a stark warning so an accidental production use is visible; got %v", getLines())
	// Bypass warning fires; the clamp warnings must NOT also fire.
	assert.False(t, anyLineContains(getLines(), "below the spec envelope floor"),
		"floor-clamp warning must NOT fire when bypass is active")
}

func TestWebhookTTL_NewWebhookRegistry_OptionPathClampsAndLogs(t *testing.T) {
	// End-to-end: NewWebhookRegistry (not just the apply method) must
	// run the clamp via the option path. Captures via the standard log
	// package isn't easy, so just verify the resulting ttl.
	r := NewWebhookRegistry(WithWebhookTTL(30 * time.Second))
	assert.Equal(t, MinWebhookTTL, r.ttl,
		"NewWebhookRegistry must clamp WithWebhookTTL even though the constructor's logger is log.Printf")
}

func anyLineContains(lines []string, needle string) bool {
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

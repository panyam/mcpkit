package server

import (
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestNegotiateProtocolVersion(t *testing.T) {
	// A supported version is echoed back verbatim.
	for _, sv := range supportedProtocolVersions {
		if got := negotiateProtocolVersion(supportedProtocolVersions, sv); got != sv {
			t.Errorf("negotiateProtocolVersion(%q) = %q, want echo", sv, got)
		}
	}
	// An unsupported version falls back to the set's preferred (first).
	if got := negotiateProtocolVersion(supportedProtocolVersions, "1999-01-01"); got != supportedProtocolVersions[0] {
		t.Errorf("unsupported -> %q, want preferred %q", got, supportedProtocolVersions[0])
	}
	// Preferred is the first entry of the given set.
	if preferredProtocolVersion(supportedProtocolVersions) != supportedProtocolVersions[0] {
		t.Errorf("preferred = %q, want %q", preferredProtocolVersion(supportedProtocolVersions), supportedProtocolVersions[0])
	}
	// A configured subset negotiates within itself: an excluded version falls
	// back to the subset's preferred, not the package default (issue 419).
	subset := []string{"2025-11-25", "2025-03-26"}
	if got := negotiateProtocolVersion(subset, "2024-11-05"); got != "2025-11-25" {
		t.Errorf("subset negotiate(2024-11-05) = %q, want 2025-11-25", got)
	}
}

func TestFeaturesForVersion(t *testing.T) {
	// Draft line activates the version-gated behaviors.
	draft := featuresForVersion(core.DraftProtocolVersion2026V1)
	if !draft.RoutingHeaderValidation || !draft.StatelessMetaRequired {
		t.Errorf("draft features = %+v, want both gated behaviors on", draft)
	}
	// Every dated release before the draft line is zero (no version gating).
	for _, v := range []string{"2025-11-25", "2025-03-26", "2024-11-05", ""} {
		if f := featuresForVersion(v); f.RoutingHeaderValidation || f.StatelessMetaRequired {
			t.Errorf("featuresForVersion(%q) = %+v, want zero", v, f)
		}
	}
}

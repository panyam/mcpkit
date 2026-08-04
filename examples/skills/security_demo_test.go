package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestSecurityDemo runs the SEP-2640 security harness end-to-end against the
// in-process skills server and gates on its own verdict: runSecurityDemo sets
// its verdict false the moment any guard fails to fire with the SEP-mandated
// sentinel (via errors.Is), so ok==true is the regression assertion. The shape
// checks below catch a step silently dropping out of the transcript.
func TestSecurityDemo(t *testing.T) {
	var buf bytes.Buffer
	ok, err := runSecurityDemo(&buf)
	out := buf.String()
	if err != nil {
		t.Fatalf("runSecurityDemo: %v\n%s", err, out)
	}
	if !ok {
		t.Fatalf("security demo reported a failed step:\n%s", out)
	}
	if strings.Contains(out, "✗") {
		t.Fatalf("transcript carries a failure marker:\n%s", out)
	}
	// The three defenses plus the unpinned-file guard: four rejections total.
	if n := strings.Count(out, "REJECT"); n != 4 {
		t.Fatalf("expected 4 REJECT outcomes, got %d:\n%s", n, out)
	}
	// Every step's anchor family must be present — a dropped step is a silent
	// coverage loss, not a failure the verdict would catch.
	for _, anchor := range []string{
		"experimental-ext-skills#85", // step 1 progressive disclosure
		"threat model B1",            // step 2 supporting-file integrity
		"threat model T6",            // step 3 byte budget
		"threat model T5",            // step 4 scheme rejection
	} {
		if !strings.Contains(out, anchor) {
			t.Fatalf("transcript missing anchor %q:\n%s", anchor, out)
		}
	}
}

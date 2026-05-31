package auth

import (
	"errors"
	"testing"
)

// TestValidateIss covers the SEP-2468 / RFC 9207 §2.4 comparison
// rules across the four upstream conformance scenarios + the
// normalization edge cases mcpkit chose to accept.
//
// Rule recap (default mode, strict=false):
//
//	iss empty + !advertised → accept (legacy AS pre-9207)
//	iss empty + advertised  → ErrIssMissing
//	iss present             → must equal expected after RFC 3986
//	                          §6.2 normalization, else ErrIssMismatch
func TestValidateIss(t *testing.T) {
	cases := []struct {
		name        string
		iss         string
		expected    string
		advertised  bool
		wantErr     error
	}{
		{
			name:       "happy: matches when AS advertised support (iss-supported scenario shape)",
			iss:        "http://localhost:18099",
			expected:   "http://localhost:18099",
			advertised: true,
			wantErr:    nil,
		},
		{
			name:       "happy: matches when AS did not advertise but sent iss anyway",
			iss:        "http://localhost:18099",
			expected:   "http://localhost:18099",
			advertised: false,
			wantErr:    nil,
		},
		{
			name:       "iss-supported-missing: AS advertised, iss omitted → reject",
			iss:        "",
			expected:   "http://localhost:18099",
			advertised: true,
			wantErr:    ErrIssMissing,
		},
		{
			name:       "legacy: AS did not advertise, iss omitted → accept (pre-9207 AS)",
			iss:        "",
			expected:   "http://localhost:18099",
			advertised: false,
			wantErr:    nil,
		},
		{
			name:       "iss-wrong-issuer: AS advertised, iss mismatches → reject",
			iss:        "https://evil.example.com",
			expected:   "http://localhost:18099",
			advertised: true,
			wantErr:    ErrIssMismatch,
		},
		{
			name:       "iss-unexpected: AS did NOT advertise but sent wrong iss → reject (presence implies validation)",
			iss:        "https://evil.example.com",
			expected:   "http://localhost:18099",
			advertised: false,
			wantErr:    ErrIssMismatch,
		},
		{
			// RFC 9207 §2.4 + upstream `sep-2468-client-no-normalization`
			// scenario: byte-for-byte comparison only. A trailing-slash
			// variant of the issuer MUST be treated as a mismatch — the
			// client doesn't get to normalize, since normalization would
			// defeat the mix-up protection the strict comparison provides.
			name:       "iss-normalized: trailing-slash variant rejects (strict per RFC 9207)",
			iss:        "http://localhost:18099/",
			expected:   "http://localhost:18099",
			advertised: true,
			wantErr:    ErrIssMismatch,
		},
		{
			name:       "iss-normalized: trailing-slash variant rejects in both directions",
			iss:        "http://localhost:18099",
			expected:   "http://localhost:18099/",
			advertised: true,
			wantErr:    ErrIssMismatch,
		},
		{
			name:       "case difference rejects (strict per RFC 9207, no scheme/host folding)",
			iss:        "HTTP://LocalHost:18099",
			expected:   "http://localhost:18099",
			advertised: true,
			wantErr:    ErrIssMismatch,
		},
		{
			name:       "path mismatch within same origin rejects",
			iss:        "http://localhost:18099/realm/A",
			expected:   "http://localhost:18099/realm/B",
			advertised: true,
			wantErr:    ErrIssMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateIss(tc.iss, tc.expected, tc.advertised)
			if tc.wantErr == nil {
				if got != nil {
					t.Fatalf("got error %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.wantErr) {
				t.Fatalf("got error %v, want errors.Is %v", got, tc.wantErr)
			}
		})
	}
}

// TestValidateIss_StringSurfaceRejected covers a malformed iss value
// (no URL semantics). Byte comparison can't accidentally match the
// expected issuer, so the case still rejects — but for a different
// reason than a parse failure would: the comparison is purely
// string-equality, never URL-aware.
func TestValidateIss_StringSurfaceRejected(t *testing.T) {
	err := validateIss("::not a url::", "http://localhost:18099", true)
	if !errors.Is(err, ErrIssMismatch) {
		t.Fatalf("got %v, want errors.Is ErrIssMismatch", err)
	}
}

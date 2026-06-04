// Package common holds tiny CLI utilities shared across mcpkit's
// single-purpose binaries (cmd/mcpskills today; cmd/mcpkit-auth and
// friends as they appear). Scope is deliberately narrow: env-var
// precedence helpers, TTY detection, and color helpers. Domain
// surfaces (mcpkit/client wrapper construction, server registration,
// etc.) live in the binaries themselves.
package common

import (
	"os"
)

// LookupURL returns the URL to target for a CLI invocation. Precedence
// (highest first):
//
//  1. flag value (passed in by the caller after cobra's flag parsing)
//  2. $<envVar> if set and non-empty
//  3. defaultURL
//
// Used by inspect-style subcommands that need a server URL with the
// usual flag-then-env-then-default pattern. Callers wire the flag.
func LookupURL(flagValue, envVar, defaultURL string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultURL
}

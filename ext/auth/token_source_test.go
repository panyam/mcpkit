package auth

import (
	"testing"

	"github.com/panyam/oneauth/client"
)

// TestValidatePKCES256_Supported verifies that PKCE validation passes when
// the AS metadata includes "S256" in code_challenge_methods_supported (C11).
func TestValidatePKCES256_Supported(t *testing.T) {
	meta := &client.ASMetadata{
		CodeChallengeMethodsSupported: []string{"plain", "S256"},
	}
	if err := ValidatePKCES256(meta); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// TestValidatePKCES256_NotSupported verifies that PKCE validation fails when
// the AS does not advertise S256 support (C12: MUST refuse to proceed).
func TestValidatePKCES256_NotSupported(t *testing.T) {
	meta := &client.ASMetadata{
		CodeChallengeMethodsSupported: []string{"plain"},
	}
	if err := ValidatePKCES256(meta); err == nil {
		t.Fatal("expected error when S256 not supported")
	}
}

// TestValidatePKCES256_Empty verifies that PKCE validation fails when
// code_challenge_methods_supported is empty (C12: MUST refuse).
func TestValidatePKCES256_Empty(t *testing.T) {
	meta := &client.ASMetadata{
		CodeChallengeMethodsSupported: []string{},
	}
	if err := ValidatePKCES256(meta); err == nil {
		t.Fatal("expected error when no methods supported")
	}
}

// TestValidatePKCES256_NilMetadata verifies that PKCE validation fails
// gracefully when AS metadata is nil.
func TestValidatePKCES256_NilMetadata(t *testing.T) {
	if err := ValidatePKCES256(nil); err == nil {
		t.Fatal("expected error for nil metadata")
	}
}

// TestValidatePKCES256_CaseInsensitive verifies that the S256 check is
// case-insensitive (some servers may return "s256" lowercase).
func TestValidatePKCES256_CaseInsensitive(t *testing.T) {
	meta := &client.ASMetadata{
		CodeChallengeMethodsSupported: []string{"s256"},
	}
	if err := ValidatePKCES256(meta); err != nil {
		t.Fatalf("S256 check should be case-insensitive, got: %v", err)
	}
}

// TestValidateCIMDURL_Valid verifies that a well-formed CIMD URL passes
// validation (C8: https with path).
func TestValidateCIMDURL_Valid(t *testing.T) {
	if err := ValidateCIMDURL("https://example.com/clients/my-app"); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

// TestValidateCIMDURL_Localhost verifies that localhost CIMD URLs are accepted
// even without https (for development/testing).
func TestValidateCIMDURL_Localhost(t *testing.T) {
	if err := ValidateCIMDURL("http://localhost:8080/clients/my-app"); err != nil {
		t.Fatalf("expected localhost exemption, got: %v", err)
	}
}

// TestValidateCIMDURL_NoHTTPS verifies that non-localhost CIMD URLs without
// https are rejected (C8: MUST use https).
func TestValidateCIMDURL_NoHTTPS(t *testing.T) {
	if err := ValidateCIMDURL("http://example.com/clients/my-app"); err == nil {
		t.Fatal("expected error for http:// URL")
	}
}

// TestValidateCIMDURL_NoPath verifies that CIMD URLs without a path component
// are rejected (C8: MUST contain path).
func TestValidateCIMDURL_NoPath(t *testing.T) {
	if err := ValidateCIMDURL("https://example.com"); err == nil {
		t.Fatal("expected error for URL without path")
	}
}

// TestValidateCIMDURL_RootPath verifies that CIMD URLs with only "/" as path
// are rejected (must have meaningful path).
func TestValidateCIMDURL_RootPath(t *testing.T) {
	if err := ValidateCIMDURL("https://example.com/"); err == nil {
		t.Fatal("expected error for root-only path")
	}
}

// TestValidateHTTPS_Localhost verifies that localhost AS endpoints are exempt
// from HTTPS enforcement (for testing).
func TestValidateHTTPS_Localhost(t *testing.T) {
	meta := &client.ASMetadata{
		AuthorizationEndpoint: "http://localhost:9000/authorize",
		TokenEndpoint:         "http://127.0.0.1:9000/token",
	}
	if err := validateHTTPS(meta); err != nil {
		t.Fatalf("localhost should be exempt from HTTPS: %v", err)
	}
}

// TestValidateHTTPS_NonLocalhost verifies that non-localhost AS endpoints
// without HTTPS are rejected (X1: MUST be HTTPS).
func TestValidateHTTPS_NonLocalhost(t *testing.T) {
	meta := &client.ASMetadata{
		AuthorizationEndpoint: "https://auth.example.com/authorize",
		TokenEndpoint:         "http://auth.example.com/token", // not HTTPS
	}
	if err := validateHTTPS(meta); err == nil {
		t.Fatal("expected error for non-HTTPS token endpoint")
	}
}

// TestValidateHTTPS_AllHTTPS verifies that all-HTTPS endpoints pass validation.
func TestValidateHTTPS_AllHTTPS(t *testing.T) {
	meta := &client.ASMetadata{
		AuthorizationEndpoint: "https://auth.example.com/authorize",
		TokenEndpoint:         "https://auth.example.com/token",
	}
	if err := validateHTTPS(meta); err != nil {
		t.Fatalf("all HTTPS should pass: %v", err)
	}
}

package core

import (
	"encoding/json"
	"errors"
	"testing"
)

// verifies: empty / nil accept matches anything (the unconstrained
// `process_any_file`-style descriptor).
func TestFileMatchesAccept_EmptyAccepts(t *testing.T) {
	if !FileMatchesAccept("image/png", "x.png", nil) {
		t.Error("nil accept should match")
	}
	if !FileMatchesAccept("image/png", "x.png", []string{}) {
		t.Error("empty accept should match")
	}
}

// verifies: exact MIME pattern matches identical type/subtype, rejects
// anything else.
func TestFileMatchesAccept_ExactMIME(t *testing.T) {
	accept := []string{"image/png"}
	if !FileMatchesAccept("image/png", "x.png", accept) {
		t.Error("exact match should succeed")
	}
	if FileMatchesAccept("image/jpeg", "x.jpg", accept) {
		t.Error("non-matching MIME should be rejected")
	}
}

// verifies: wildcard subtype ("image/*") matches every image/... and
// rejects everything outside that prefix.
func TestFileMatchesAccept_WildcardSubtype(t *testing.T) {
	accept := []string{"image/*"}
	if !FileMatchesAccept("image/png", "x.png", accept) {
		t.Error("image/png should match image/*")
	}
	if !FileMatchesAccept("image/jpeg", "x.jpg", accept) {
		t.Error("image/jpeg should match image/*")
	}
	if FileMatchesAccept("application/pdf", "x.pdf", accept) {
		t.Error("application/pdf must not match image/*")
	}
}

// verifies: extension hint matches by filename suffix, case-insensitive.
// Server-side matcher must agree with the JS-side bridge matcher.
func TestFileMatchesAccept_ExtensionHint(t *testing.T) {
	accept := []string{".pdf"}
	if !FileMatchesAccept("application/pdf", "report.pdf", accept) {
		t.Error("lowercase extension should match")
	}
	if !FileMatchesAccept("application/pdf", "REPORT.PDF", accept) {
		t.Error("uppercase filename should match (case-insensitive)")
	}
	if FileMatchesAccept("application/pdf", "report.txt", accept) {
		t.Error("non-matching extension should be rejected")
	}
}

// verifies: when accept includes both extension and MIME forms, either
// shape matches (the analyze_documents demo tool uses both).
func TestFileMatchesAccept_MixedPatterns(t *testing.T) {
	accept := []string{"application/pdf", ".pdf"}
	if !FileMatchesAccept("application/pdf", "x.pdf", accept) {
		t.Error("MIME form should match")
	}
	// File without proper MIME but with .pdf extension still matches via the
	// extension fallback.
	if !FileMatchesAccept("application/octet-stream", "report.pdf", accept) {
		t.Error("extension fallback should match when MIME is generic")
	}
}

// verifies: nil descriptor short-circuits — unconstrained tools accept
// every file without decoding.
func TestValidateFileInput_NilDescriptor(t *testing.T) {
	if err := ValidateFileInput("data:text/plain;base64,aGVsbG8=", nil); err != nil {
		t.Errorf("nil descriptor must not error: %v", err)
	}
}

// verifies: a valid file passes both size and MIME checks.
func TestValidateFileInput_HappyPath(t *testing.T) {
	max := 1024
	desc := &FileInputDescriptor{
		Accept:  []string{"text/plain"},
		MaxSize: &max,
	}
	uri := EncodeDataURI([]byte("hello"), "text/plain", "greeting.txt")
	if err := ValidateFileInput(uri, desc); err != nil {
		t.Errorf("happy-path validation failed: %v", err)
	}
}

// verifies: oversized payload returns *FileTooLargeError with the actual
// size + declared limit, and that errors.Is(err, ErrFileTooLarge) holds
// so callers can branch on the sentinel.
func TestValidateFileInput_OversizedReturnsTypedError(t *testing.T) {
	max := 4
	desc := &FileInputDescriptor{MaxSize: &max}
	uri := EncodeDataURI([]byte("hello world"), "text/plain", "x.txt")
	err := ValidateFileInput(uri, desc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("errors.Is(err, ErrFileTooLarge) = false; err = %T %v", err, err)
	}
	tooBig, ok := err.(*FileTooLargeError)
	if !ok {
		t.Fatalf("err type = %T, want *FileTooLargeError", err)
	}
	if tooBig.ActualSize != 11 || tooBig.MaxSize != 4 {
		t.Errorf("actual = %d / max = %d, want 11 / 4", tooBig.ActualSize, tooBig.MaxSize)
	}
}

// verifies: wrong MIME returns *FileTypeNotAcceptedError carrying the
// observed media type + the accept list (so clients can render a useful
// error message).
func TestValidateFileInput_WrongMIMEReturnsTypedError(t *testing.T) {
	desc := &FileInputDescriptor{Accept: []string{"image/*"}}
	uri := EncodeDataURI([]byte("hello"), "text/plain", "x.txt")
	err := ValidateFileInput(uri, desc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrFileTypeNotAccepted) {
		t.Errorf("errors.Is(err, ErrFileTypeNotAccepted) = false; err = %T %v", err, err)
	}
	mismatch, ok := err.(*FileTypeNotAcceptedError)
	if !ok {
		t.Fatalf("err type = %T, want *FileTypeNotAcceptedError", err)
	}
	if mismatch.MediaType != "text/plain" {
		t.Errorf("MediaType = %q, want text/plain", mismatch.MediaType)
	}
	if len(mismatch.Accept) != 1 || mismatch.Accept[0] != "image/*" {
		t.Errorf("Accept = %v, want [image/*]", mismatch.Accept)
	}
}

// verifies: extension-fallback works through ValidateFileInput, not just
// FileMatchesAccept directly. Sends a generic-MIME PDF (matches via
// .pdf extension) — guards against a regression where the orchestrator
// only checks MIME and ignores the filename.
func TestValidateFileInput_ExtensionFallback(t *testing.T) {
	desc := &FileInputDescriptor{Accept: []string{"application/pdf", ".pdf"}}
	uri := EncodeDataURI([]byte("%PDF-1.4\n"), "application/octet-stream", "doc.pdf")
	if err := ValidateFileInput(uri, desc); err != nil {
		t.Errorf("extension fallback should accept: %v", err)
	}
}

// verifies: malformed data URIs surface the underlying decoder error
// rather than a generic validation error. The dispatcher will report
// these as -32602 too, but with the decoder's reason string so debug
// signal stays high.
func TestValidateFileInput_MalformedURI(t *testing.T) {
	desc := &FileInputDescriptor{}
	if err := ValidateFileInput("not-a-data-uri", desc); err == nil {
		t.Error("malformed URI should error")
	}
	if err := ValidateFileInput("data:text/plain,not-base64-form", desc); err == nil {
		t.Error("non-base64 data URI should error")
	}
}

// verifies: typed-error Data() methods produce JSON matching the wire
// shape locked by the conformance suite (`{reason, actualSize, maxSize}`
// and `{reason, mediaType, accept}`). If this test fails, the
// `conformance/file-inputs/` scenarios will too — they assert the same
// keys on the JSON-RPC error.data object.
func TestFileInputErrorData_WireShape(t *testing.T) {
	tooBig := (&FileTooLargeError{ActualSize: 100, MaxSize: 64}).Data()
	raw, _ := json.Marshal(tooBig)
	if string(raw) != `{"reason":"file_too_large","actualSize":100,"maxSize":64}` {
		t.Errorf("FileTooLargeData JSON = %s", raw)
	}

	wrongType := (&FileTypeNotAcceptedError{
		MediaType: "text/plain",
		Accept:    []string{"image/*"},
	}).Data()
	raw, _ = json.Marshal(wrongType)
	if string(raw) != `{"reason":"file_type_not_accepted","mediaType":"text/plain","accept":["image/*"]}` {
		t.Errorf("FileTypeNotAcceptedData JSON = %s", raw)
	}

	// nil Accept marshals to [] (not null) so JS-side type checks pass.
	emptyAccept := (&FileTypeNotAcceptedError{MediaType: "x/y"}).Data()
	raw, _ = json.Marshal(emptyAccept)
	if string(raw) != `{"reason":"file_type_not_accepted","mediaType":"x/y","accept":[]}` {
		t.Errorf("nil Accept JSON = %s, want [] not null", raw)
	}
}

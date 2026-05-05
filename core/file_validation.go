package core

import (
	"errors"
	"fmt"
	"strings"
)

// SEP-2356 file-input validation — runs against a data URI argument and the
// server-declared FileInputDescriptor. Returns one of the typed errors
// below on failure; nil on success or when desc is nil (no constraint).
//
// The error types carry structured fields the dispatcher packs into the
// JSON-RPC `data` object on the wire — the on-wire shape is frozen by
// `conformance/file-inputs/scenarios.test.ts` (the cross-impl contract).

// Sentinel errors for callers that just want to test category. The typed
// errors below carry structured fields; these constants are useful for
// `errors.Is` checks.
var (
	ErrFileTooLarge        = errors.New("file too large")
	ErrFileTypeNotAccepted = errors.New("file type not accepted")
)

// File-input error reason strings. Pinned by the conformance suite as
// stable machine-readable identifiers — clients can branch on these
// without parsing the human-readable message. JS-side bridge errors
// (ext/ui/assets/file-picker.ts) use the same constants.
const (
	FileInputReasonTooLarge       = "file_too_large"
	FileInputReasonTypeNotAccepted = "file_type_not_accepted"
)

// FileTooLargeError reports that a decoded payload exceeds the
// descriptor's MaxSize. The dispatcher serializes Data() as the JSON-RPC
// `error.data` object on the wire.
type FileTooLargeError struct {
	// Field is the JSON-Schema property path of the offending arg (e.g.
	// "image" or "documents[0]"). Optional — empty when the validator
	// doesn't have path context.
	Field      string
	ActualSize int
	MaxSize    int
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("file size %d exceeds maxSize %d", e.ActualSize, e.MaxSize)
}

// Unwrap lets errors.Is(err, ErrFileTooLarge) succeed.
func (e *FileTooLargeError) Unwrap() error { return ErrFileTooLarge }

// FileTooLargeData is the wire-shape of FileTooLargeError. Frozen by the
// conformance suite: `{reason, actualSize, maxSize}`.
type FileTooLargeData struct {
	Reason     string `json:"reason"`     // always "file_too_large"
	Field      string `json:"field,omitempty"`
	ActualSize int    `json:"actualSize"`
	MaxSize    int    `json:"maxSize"`
}

// Data returns the structured wire payload for this error.
func (e *FileTooLargeError) Data() FileTooLargeData {
	return FileTooLargeData{
		Reason:     FileInputReasonTooLarge,
		Field:      e.Field,
		ActualSize: e.ActualSize,
		MaxSize:    e.MaxSize,
	}
}

// FileTypeNotAcceptedError reports that a decoded payload's media type or
// filename does not match any pattern in the descriptor's Accept list.
type FileTypeNotAcceptedError struct {
	Field     string
	MediaType string
	Filename  string
	Accept    []string
}

func (e *FileTypeNotAcceptedError) Error() string {
	return fmt.Sprintf("file type %q not in accept list %v", e.MediaType, e.Accept)
}

// Unwrap lets errors.Is(err, ErrFileTypeNotAccepted) succeed.
func (e *FileTypeNotAcceptedError) Unwrap() error { return ErrFileTypeNotAccepted }

// FileTypeNotAcceptedData is the wire-shape of FileTypeNotAcceptedError.
// Frozen by the conformance suite: `{reason, mediaType, accept}`.
type FileTypeNotAcceptedData struct {
	Reason    string   `json:"reason"`     // always "file_type_not_accepted"
	Field     string   `json:"field,omitempty"`
	MediaType string   `json:"mediaType"`
	Filename  string   `json:"filename,omitempty"`
	Accept    []string `json:"accept"`
}

// Data returns the structured wire payload for this error.
func (e *FileTypeNotAcceptedError) Data() FileTypeNotAcceptedData {
	accept := e.Accept
	if accept == nil {
		// JSON-marshal nil to [] not null — the conformance suite asserts
		// `data.accept` is an array.
		accept = []string{}
	}
	return FileTypeNotAcceptedData{
		Reason:    FileInputReasonTypeNotAccepted,
		Field:     e.Field,
		MediaType: e.MediaType,
		Filename:  e.Filename,
		Accept:    accept,
	}
}

// FileMatchesAccept implements the SEP-2356 accept-pattern matcher.
// Pattern rules — same set as the JS-side bridge matcher in
// ext/ui/assets/file-picker.ts so both sides agree on what passes:
//
//   - "image/png"  — exact MIME match (case-sensitive on type, as RFC 6838)
//   - "image/*"    — wildcard subtype match (prefix on `type/`)
//   - ".pdf"       — extension match against filename suffix (case-insensitive)
//
// Empty / nil accept means anything matches.
func FileMatchesAccept(mediaType, filename string, accept []string) bool {
	if len(accept) == 0 {
		return true
	}
	lowerName := strings.ToLower(filename)
	for _, pattern := range accept {
		if strings.HasPrefix(pattern, ".") {
			if strings.HasSuffix(lowerName, strings.ToLower(pattern)) {
				return true
			}
			continue
		}
		slash := strings.Index(pattern, "/")
		if slash < 0 {
			continue
		}
		subtype := pattern[slash+1:]
		if subtype == "*" {
			if strings.HasPrefix(mediaType, pattern[:slash+1]) {
				return true
			}
		} else if mediaType == pattern {
			return true
		}
	}
	return false
}

// ValidateFileInput decodes a data URI argument and verifies it against
// a server-declared FileInputDescriptor.
//
//   - Returns nil on success, or when desc is nil (no constraint).
//   - Returns *FileTooLargeError when MaxSize is set and exceeded.
//   - Returns *FileTypeNotAcceptedError when Accept is set and unmet.
//   - Returns the underlying decoder error when uri is malformed.
//
// The returned typed errors carry structured fields that the dispatcher
// packs into the JSON-RPC `data` object — see the conformance suite for
// the frozen wire shape.
func ValidateFileInput(uri string, desc *FileInputDescriptor) error {
	if desc == nil {
		return nil
	}
	data, mediaType, filename, err := DecodeDataURI(uri)
	if err != nil {
		return err
	}
	if desc.MaxSize != nil && len(data) > *desc.MaxSize {
		return &FileTooLargeError{
			ActualSize: len(data),
			MaxSize:    *desc.MaxSize,
		}
	}
	if !FileMatchesAccept(mediaType, filename, desc.Accept) {
		return &FileTypeNotAcceptedError{
			MediaType: mediaType,
			Filename:  filename,
			Accept:    desc.Accept,
		}
	}
	return nil
}

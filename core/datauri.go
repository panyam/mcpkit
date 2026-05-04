package core

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// SEP-2356 file inputs are passed as RFC 2397 data URIs, restricted to the
// base64-encoded form with an optional `name=` parameter carrying the
// percent-encoded original filename:
//
//	data:<mediatype>;name=<pct-encoded-filename>;base64,<data>
//
// The plain (non-base64) data-URI form defined by RFC 2397 is intentionally
// unsupported: the spec mandates base64 because file payloads are binary
// and binary in URL-encoded form is wasteful and ambiguous.

// DataURIPrefix is the literal prefix that all data URIs share.
const DataURIPrefix = "data:"

// DataURIDefaultMediaType is the media type assumed when a data URI omits
// the mediatype component (per RFC 2397 §3).
const DataURIDefaultMediaType = "text/plain;charset=US-ASCII"

// Sentinel errors returned by DecodeDataURI.
var (
	ErrNotDataURI         = errors.New("not a data URI")
	ErrMalformedDataURI   = errors.New("malformed data URI")
	ErrNonBase64DataURI   = errors.New("data URI is not base64-encoded")
	ErrInvalidDataURIName = errors.New("data URI name parameter is malformed")
)

// IsDataURI reports whether s starts with the "data:" scheme prefix.
// Cheap prefix check; use DecodeDataURI to actually parse.
func IsDataURI(s string) bool {
	return strings.HasPrefix(s, DataURIPrefix)
}

// EncodeDataURI builds an RFC 2397 base64 data URI suitable for SEP-2356
// file inputs. mediaType is embedded verbatim (e.g. "image/png"); pass an
// empty string to omit it (the consumer will assume text/plain;charset=US-ASCII
// per RFC 2397). filename is optional; if non-empty it is percent-encoded
// and emitted as a `name=` parameter.
func EncodeDataURI(data []byte, mediaType, filename string) string {
	var sb strings.Builder
	sb.WriteString(DataURIPrefix)
	sb.WriteString(mediaType)
	if filename != "" {
		sb.WriteString(";name=")
		sb.WriteString(url.PathEscape(filename))
	}
	sb.WriteString(";base64,")
	sb.WriteString(base64.StdEncoding.EncodeToString(data))
	return sb.String()
}

// DecodeDataURI parses an RFC 2397 base64 data URI as produced by
// EncodeDataURI. Returns the decoded payload, the media type (with the
// default substituted when omitted), and the decoded filename from any
// `name=` parameter (empty when absent).
//
// Non-base64 data URIs are rejected with ErrNonBase64DataURI; SEP-2356
// requires the base64 form because payloads are binary.
func DecodeDataURI(uri string) (data []byte, mediaType, filename string, err error) {
	if !IsDataURI(uri) {
		return nil, "", "", ErrNotDataURI
	}
	body := uri[len(DataURIPrefix):]
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return nil, "", "", ErrMalformedDataURI
	}
	header, payload := body[:comma], body[comma+1:]

	parts := strings.Split(header, ";")
	hasBase64 := false
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "base64" {
			hasBase64 = true
			parts = append(parts[:i], parts[i+1:]...)
			break
		}
	}
	if !hasBase64 {
		return nil, "", "", ErrNonBase64DataURI
	}

	mediaType = ""
	if len(parts) > 0 && parts[0] != "" && !strings.Contains(parts[0], "=") {
		mediaType = parts[0]
		parts = parts[1:]
	}
	if mediaType == "" {
		mediaType = DataURIDefaultMediaType
	}

	for _, p := range parts {
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			// Bare parameter keys aren't part of SEP-2356 — preserve
			// them in mediaType to stay round-trippable when the
			// payload happens to contain unknown extensions.
			mediaType += ";" + p
			continue
		}
		key, value := p[:eq], p[eq+1:]
		if key == "name" {
			decoded, decodeErr := url.PathUnescape(value)
			if decodeErr != nil {
				return nil, "", "", ErrInvalidDataURIName
			}
			filename = decoded
			continue
		}
		mediaType += ";" + p
	}

	decoded, decodeErr := base64.StdEncoding.DecodeString(payload)
	if decodeErr != nil {
		// Fall back to the URL-safe alphabet for tolerance — encoders
		// occasionally emit "-" / "_" instead of "+" / "/".
		decoded, decodeErr = base64.RawStdEncoding.DecodeString(payload)
	}
	if decodeErr != nil {
		return nil, "", "", ErrMalformedDataURI
	}
	return decoded, mediaType, filename, nil
}

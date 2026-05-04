package core

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeDataURI(t *testing.T) {
	got := EncodeDataURI([]byte("hello"), "text/plain", "")
	want := "data:text/plain;base64,aGVsbG8="
	if got != want {
		t.Errorf("EncodeDataURI = %q, want %q", got, want)
	}
}

func TestEncodeDataURIWithFilename(t *testing.T) {
	got := EncodeDataURI([]byte("hello"), "text/plain", "report.txt")
	want := "data:text/plain;name=report.txt;base64,aGVsbG8="
	if got != want {
		t.Errorf("EncodeDataURI = %q, want %q", got, want)
	}
}

func TestEncodeDataURIPercentEncodesFilename(t *testing.T) {
	got := EncodeDataURI([]byte("x"), "image/png", "my photo (1).png")
	if !strings.Contains(got, ";name=my%20photo%20%281%29.png;") {
		t.Errorf("filename was not percent-encoded: %s", got)
	}
}

func TestDecodeDataURIRoundTrip(t *testing.T) {
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 0x42}
	uri := EncodeDataURI(payload, "application/octet-stream", "weird name+%.bin")

	gotData, gotMedia, gotName, err := DecodeDataURI(uri)
	if err != nil {
		t.Fatalf("DecodeDataURI: %v", err)
	}
	if !bytes.Equal(gotData, payload) {
		t.Errorf("data = %v, want %v", gotData, payload)
	}
	if gotMedia != "application/octet-stream" {
		t.Errorf("mediaType = %q", gotMedia)
	}
	if gotName != "weird name+%.bin" {
		t.Errorf("filename = %q, want %q", gotName, "weird name+%.bin")
	}
}

func TestDecodeDataURIWithoutFilename(t *testing.T) {
	uri := "data:image/png;base64,aGVsbG8="
	data, media, name, err := DecodeDataURI(uri)
	if err != nil {
		t.Fatalf("DecodeDataURI: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q", data)
	}
	if media != "image/png" {
		t.Errorf("mediaType = %q", media)
	}
	if name != "" {
		t.Errorf("filename = %q, expected empty", name)
	}
}

func TestDecodeDataURIDefaultMediaType(t *testing.T) {
	uri := "data:;base64,aGVsbG8="
	_, media, _, err := DecodeDataURI(uri)
	if err != nil {
		t.Fatalf("DecodeDataURI: %v", err)
	}
	if media != DataURIDefaultMediaType {
		t.Errorf("mediaType = %q, want %q", media, DataURIDefaultMediaType)
	}
}

func TestDecodeDataURIRejectsNonBase64(t *testing.T) {
	// RFC 2397 plain (URL-encoded) form — must be rejected per SEP-2356.
	uri := "data:text/plain,hello%20world"
	if _, _, _, err := DecodeDataURI(uri); err != ErrNonBase64DataURI {
		t.Errorf("err = %v, want ErrNonBase64DataURI", err)
	}
}

func TestDecodeDataURIRejectsNonDataScheme(t *testing.T) {
	if _, _, _, err := DecodeDataURI("https://example.com/x"); err != ErrNotDataURI {
		t.Errorf("err = %v, want ErrNotDataURI", err)
	}
}

func TestDecodeDataURIRejectsMissingComma(t *testing.T) {
	if _, _, _, err := DecodeDataURI("data:text/plain;base64"); err != ErrMalformedDataURI {
		t.Errorf("err = %v, want ErrMalformedDataURI", err)
	}
}

func TestDecodeDataURIRejectsBadBase64(t *testing.T) {
	if _, _, _, err := DecodeDataURI("data:text/plain;base64,!!!"); err != ErrMalformedDataURI {
		t.Errorf("err = %v, want ErrMalformedDataURI", err)
	}
}

func TestDecodeDataURIPreservesMediaTypeParams(t *testing.T) {
	// Unknown parameters should ride along on mediaType so the caller
	// retains them for re-emission.
	uri := "data:text/plain;charset=utf-8;base64,aGVsbG8="
	_, media, _, err := DecodeDataURI(uri)
	if err != nil {
		t.Fatalf("DecodeDataURI: %v", err)
	}
	if media != "text/plain;charset=utf-8" {
		t.Errorf("mediaType = %q, want %q", media, "text/plain;charset=utf-8")
	}
}

func TestIsDataURI(t *testing.T) {
	if !IsDataURI("data:image/png;base64,abc") {
		t.Error("IsDataURI false for valid prefix")
	}
	if IsDataURI("https://example.com") {
		t.Error("IsDataURI true for non-data scheme")
	}
	if IsDataURI("") {
		t.Error("IsDataURI true for empty string")
	}
}

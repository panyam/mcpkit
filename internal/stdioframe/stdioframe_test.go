package stdioframe

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, r *Reader) []string {
	t.Helper()
	var got []string
	for {
		msg, err := r.Read()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		got = append(got, string(msg))
	}
}

func TestAutoDetectsNewlineDelimited(t *testing.T) {
	r := NewReader(strings.NewReader("\n  {\"a\":1}\r\n\n[1,2]\n{\"b\":2}"), Auto)
	got := readAll(t, r)
	want := []string{`{"a":1}`, `[1,2]`, `{"b":2}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
	if r.Framing() != NewlineDelimited {
		t.Errorf("Framing() = %v, want newline-delimited", r.Framing())
	}
}

func TestAutoDetectsContentLength(t *testing.T) {
	var in bytes.Buffer
	Write(&in, ContentLength, []byte(`{"a":1}`))
	Write(&in, ContentLength, []byte("{\n\"b\":2}"))
	r := NewReader(&in, Auto)
	got := readAll(t, r)
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != "{\n\"b\":2}" {
		t.Errorf("got %q", got)
	}
	if r.Framing() != ContentLength {
		t.Errorf("Framing() = %v, want content-length", r.Framing())
	}
}

func TestFramingUnresolvedBeforeFirstRead(t *testing.T) {
	r := NewReader(strings.NewReader(""), Auto)
	if r.Framing() != Auto {
		t.Errorf("Framing() = %v before any read, want auto", r.Framing())
	}
	if _, err := r.Read(); err != io.EOF {
		t.Errorf("Read on empty stream = %v, want io.EOF", err)
	}
}

// A message that reaches Write with a line break in it, as a raw pushed
// request can, still goes out as one line.
func TestWriteNewlineDelimitedCompactsLineBreaks(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, NewlineDelimited, []byte("{\r\n  \"a\": \"x\\ny\"\n}")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := out.String(), "{\"a\":\"x\\ny\"}\n"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestWriteAutoIsNewlineDelimited(t *testing.T) {
	var out bytes.Buffer
	Write(&out, Auto, []byte(`{"a":1}`))
	if out.String() != "{\"a\":1}\n" {
		t.Errorf("wrote %q", out.String())
	}
}

func TestWriteNewlineDelimitedRejectsInvalidMultilineJSON(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, NewlineDelimited, []byte("{\n")); err == nil {
		t.Error("want error for a multi-line message that is not JSON")
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q on error", out.String())
	}
}

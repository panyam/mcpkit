package files

import (
	"errors"
	"strings"
	"testing"
)

const src = `package main

func greet() string {
	return "hello"
}

func farewell() string {
	return "goodbye"
}
`

// TestStaleContentIsRejected is the property the package exists for. The
// anchor still matches, so every mechanism that only locates text would apply
// this edit happily; only the hash knows the caller was looking at something
// else.
func TestStaleContentIsRejected(t *testing.T) {
	e := Edit{
		ExpectHash: Hash("the file as it was read"),
		Hunks:      []Hunk{{Old: `"hello"`, New: `"hi"`}},
	}
	got, err := e.Apply(src)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("Apply() error = %v, want ErrStale", err)
	}
	if got != "" {
		t.Errorf("Apply() returned %q on failure, want no partial result", got)
	}
}

func TestMatchingHashApplies(t *testing.T) {
	e := Edit{
		ExpectHash: Hash(src),
		Hunks:      []Hunk{{Old: `"hello"`, New: `"hi"`}},
	}
	got, err := e.Apply(src)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if !strings.Contains(got, `return "hi"`) {
		t.Errorf("Apply() did not apply the hunk:\n%s", got)
	}
}

func TestEmptyExpectHashSkipsTheCheck(t *testing.T) {
	e := Edit{Hunks: []Hunk{{Old: `"hello"`, New: `"hi"`}}}
	if _, err := e.Apply(src); err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
}

// TestAmbiguousAnchorIsRejected pins that a repeated anchor is an error rather
// than a first-match-wins guess. Picking one would apply the edit somewhere the
// caller did not look at.
func TestAmbiguousAnchorIsRejected(t *testing.T) {
	e := Edit{Hunks: []Hunk{{Old: "string {", New: "string { // edited"}}}
	_, err := e.Apply(src)
	if !errors.Is(err, ErrAnchorAmbiguous) {
		t.Fatalf("Apply() error = %v, want ErrAnchorAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "2 places") {
		t.Errorf("error should report how many matches, got: %v", err)
	}
}

func TestUniqueMultilineAnchorApplies(t *testing.T) {
	e := Edit{Hunks: []Hunk{{
		Old: "func farewell() string {\n\treturn \"goodbye\"\n}",
		New: "func farewell() string {\n\treturn \"bye\"\n}",
	}}}
	got, err := e.Apply(src)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if !strings.Contains(got, `return "bye"`) || strings.Contains(got, `return "goodbye"`) {
		t.Errorf("Apply() did not replace the block:\n%s", got)
	}
}

func TestMissingAnchorIsRejected(t *testing.T) {
	e := Edit{Hunks: []Hunk{{Old: `"never in the file"`, New: "x"}}}
	if _, err := e.Apply(src); !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("Apply() error = %v, want ErrAnchorNotFound", err)
	}
}

// TestOneBadHunkAbandonsTheWholeEdit is the all-or-nothing property. The first
// hunk is perfectly valid; applying it and reporting the second as failed would
// leave the file in a state no caller asked for.
func TestOneBadHunkAbandonsTheWholeEdit(t *testing.T) {
	e := Edit{Hunks: []Hunk{
		{Old: `"hello"`, New: `"hi"`},
		{Old: `"not present"`, New: "x"},
	}}
	got, err := e.Apply(src)
	if !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("Apply() error = %v, want ErrAnchorNotFound", err)
	}
	if got != "" {
		t.Errorf("Apply() returned %q on failure, want no partial result", got)
	}
}

// TestAnchorsResolveAgainstTheOriginal guards order-independence at its root.
// The second anchor exists only in what the first hunk produces, so a
// sequential implementation would apply it and this would pass silently.
func TestAnchorsResolveAgainstTheOriginal(t *testing.T) {
	e := Edit{Hunks: []Hunk{
		{Old: `"hello"`, New: `"aloha"`},
		{Old: `"aloha"`, New: `"ciao"`},
	}}
	if _, err := e.Apply(src); !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("Apply() error = %v, want ErrAnchorNotFound", err)
	}
}

func TestHunkOrderDoesNotChangeTheResult(t *testing.T) {
	first := Hunk{Old: `"hello"`, New: `"hi"`}
	second := Hunk{Old: `"goodbye"`, New: `"bye"`}

	forward, err := Edit{Hunks: []Hunk{first, second}}.Apply(src)
	if err != nil {
		t.Fatalf("forward order error = %v", err)
	}
	reverse, err := Edit{Hunks: []Hunk{second, first}}.Apply(src)
	if err != nil {
		t.Fatalf("reverse order error = %v", err)
	}
	if forward != reverse {
		t.Errorf("order changed the result:\n%q\nvs\n%q", forward, reverse)
	}
	if !strings.Contains(forward, `"hi"`) || !strings.Contains(forward, `"bye"`) {
		t.Errorf("both hunks should have applied:\n%s", forward)
	}
}

func TestOverlappingHunksAreRejected(t *testing.T) {
	e := Edit{Hunks: []Hunk{
		{Old: `return "hello"`, New: `return "hi"`},
		{Old: `"hello"`, New: `"aloha"`},
	}}
	if _, err := e.Apply(src); !errors.Is(err, ErrOverlap) {
		t.Fatalf("Apply() error = %v, want ErrOverlap", err)
	}
}

// TestIdenticalHunksOverlap covers the degenerate case of the same edit listed
// twice: both resolve to the one occurrence, so they collide with each other.
func TestIdenticalHunksOverlap(t *testing.T) {
	h := Hunk{Old: `"hello"`, New: `"hi"`}
	if _, err := (Edit{Hunks: []Hunk{h, h}}).Apply(src); !errors.Is(err, ErrOverlap) {
		t.Fatalf("Apply() error = %v, want ErrOverlap", err)
	}
}

func TestEmptyNewDeletesTheAnchor(t *testing.T) {
	e := Edit{Hunks: []Hunk{{Old: "\nfunc farewell() string {\n\treturn \"goodbye\"\n}\n"}}}
	got, err := e.Apply(src)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if strings.Contains(got, "farewell") {
		t.Errorf("Apply() should have deleted the block:\n%s", got)
	}
}

// TestEmptyAnchorIsMalformedNotAmbiguous separates the two: an empty string is
// found at every offset, so reporting it as ambiguous would send the caller off
// to add context to an anchor that does not exist.
func TestEmptyAnchorIsMalformedNotAmbiguous(t *testing.T) {
	e := Edit{Hunks: []Hunk{{Old: "", New: "x"}}}
	if _, err := e.Apply(src); !errors.Is(err, ErrEmptyAnchor) {
		t.Fatalf("Apply() error = %v, want ErrEmptyAnchor", err)
	}
}

func TestNoHunksIsRejected(t *testing.T) {
	if _, err := (Edit{}).Apply(src); !errors.Is(err, ErrNoHunks) {
		t.Fatalf("Apply() error = %v, want ErrNoHunks", err)
	}
}

// TestStaleIsCheckedBeforeAnchors keeps the reported cause the actionable one.
// Both faults are present; if anchors were resolved first the caller would be
// told to fix an anchor that is fine in the file they have not read yet.
func TestStaleIsCheckedBeforeAnchors(t *testing.T) {
	e := Edit{
		ExpectHash: Hash("something else"),
		Hunks:      []Hunk{{Old: "not present either", New: "x"}},
	}
	if _, err := e.Apply(src); !errors.Is(err, ErrStale) {
		t.Fatalf("Apply() error = %v, want ErrStale", err)
	}
}

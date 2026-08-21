package exec

import (
	"strings"
	"testing"
)

func TestCapBufferKeepsEverythingUnderTheCap(t *testing.T) {
	b := newCapBuffer(100)
	b.Write([]byte("hello"))
	if got := b.String(); got != "hello" {
		t.Errorf("String() = %q, want hello", got)
	}
	if b.Truncated() {
		t.Error("nothing was dropped")
	}
}

func TestCapBufferDropsTheMiddleAndCountsIt(t *testing.T) {
	b := newCapBuffer(100)
	b.Write([]byte(strings.Repeat("H", 60)))
	b.Write([]byte(strings.Repeat("M", 500)))
	b.Write([]byte(strings.Repeat("T", 40)))

	got := b.String()
	if !strings.HasPrefix(got, strings.Repeat("H", 50)) {
		t.Errorf("the head must survive, got %q", got)
	}
	if !strings.HasSuffix(got, strings.Repeat("T", 40)) {
		t.Errorf("the tail must survive, got %q", got)
	}
	if !b.Truncated() {
		t.Error("Truncated() must report the drop")
	}
	if !strings.Contains(got, "bytes of output dropped") {
		t.Errorf("the drop must be stated, got %q", got)
	}
}

func TestCapBufferKeepsTheEndOfAStreamThatOnlyGrows(t *testing.T) {
	b := newCapBuffer(100)
	for i := 0; i < 200; i++ {
		b.Write([]byte("0123456789"))
	}
	got := b.String()
	if !strings.HasSuffix(got, "0123456789") {
		t.Errorf("the newest output must be at the end, got %q", got)
	}
	if len(got) > 300 {
		t.Errorf("the cap is not holding: %d bytes", len(got))
	}
}

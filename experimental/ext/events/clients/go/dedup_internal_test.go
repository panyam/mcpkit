package eventsclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDeduper_ForgetsAfterWindow(t *testing.T) {
	now := time.Unix(0, 0)
	d := NewDeduper(10, time.Minute)
	d.now = func() time.Time { return now }

	assert.False(t, d.Seen("a"))
	now = now.Add(59 * time.Second)
	assert.True(t, d.Seen("a"))
	now = now.Add(2 * time.Second)
	assert.False(t, d.Seen("a"), "an id older than the window is delivered again")
}

func TestDeduper_EvictsOldestAtCapacity(t *testing.T) {
	d := NewDeduper(2, time.Hour)
	d.Seen("a")
	d.Seen("b")
	d.Seen("c")
	assert.False(t, d.Seen("a"), "a was evicted when c arrived")
	assert.True(t, d.Seen("c"))
}

func TestDeduper_EmptyKeyIsNeverDeduped(t *testing.T) {
	d := NewDeduper(0, 0)
	assert.False(t, d.Seen(""))
	assert.False(t, d.Seen(""))
}

package exec

import (
	"bytes"
	"fmt"
	"sync"
)

// capBuffer collects a stream and keeps at most max bytes of it, dropping from
// the middle.
//
// The middle is what to drop. A failing build states the problem in the first
// lines (a config error, a missing tool) or the last ones (the failure
// summary, the assertion), and a tail-only buffer loses the first kind while a
// head-only one loses the second.
//
// It is written to from the goroutines os/exec runs for stdout and stderr, so
// it locks.
type capBuffer struct {
	mu      sync.Mutex
	max     int
	head    bytes.Buffer
	tail    []byte
	dropped int
}

func newCapBuffer(max int) *capBuffer {
	return &capBuffer{max: max}
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)

	half := c.max / 2
	if room := half - c.head.Len(); room > 0 {
		take := min(room, len(p))
		c.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}

	// Everything past the head is a rolling window of the last half bytes.
	c.tail = append(c.tail, p...)
	if excess := len(c.tail) - (c.max - half); excess > 0 {
		c.tail = c.tail[excess:]
		c.dropped += excess
	}
	return n, nil
}

// String renders what was kept, with a line naming what was not. The count is
// stated because a model that cannot tell truncated output from complete
// output will read a passing tail as a passing run.
func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return c.head.String() + string(c.tail)
	}
	return fmt.Sprintf("%s\n... %d bytes of output dropped from the middle ...\n%s",
		c.head.String(), c.dropped, string(c.tail))
}

// Truncated reports whether anything was dropped.
func (c *capBuffer) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped > 0
}

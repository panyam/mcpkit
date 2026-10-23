package eventsclient

import (
	"container/list"
	"sync"
	"time"
)

// Default bounds for a Deduper built by NewDeduper with zero arguments.
const (
	DefaultDedupCapacity = 10_000
	DefaultDedupWindow   = 24 * time.Hour
)

// Deduper is a bounded seen-set of event ids. The server mints an eventId once
// at yield and every copy carries it (poll page, push frame, webhook POST,
// retry, backfill after a gap), so a second sighting of the same id is the same
// occurrence arriving again (#1558).
//
// Poll, Stream, StreamChan and Receiver each dedup by default with a private
// Deduper. Pass one shared Deduper to several of them to also drop the overlap
// between delivery modes, e.g. a stream's push frames and the poll backfill
// that follows a reconnect.
//
// Ids are forgotten after the window, or oldest first once capacity is
// reached, so a duplicate arriving later than either bound is delivered again.
// Safe for concurrent use.
type Deduper struct {
	mu       sync.Mutex
	capacity int
	window   time.Duration
	now      func() time.Time
	seen     map[string]*list.Element
	order    *list.List // front is oldest
}

type seenEntry struct {
	key string
	at  time.Time
}

// NewDeduper returns a Deduper holding at most capacity ids for at most
// window. Zero (or negative) values take DefaultDedupCapacity and
// DefaultDedupWindow.
func NewDeduper(capacity int, window time.Duration) *Deduper {
	if capacity <= 0 {
		capacity = DefaultDedupCapacity
	}
	if window <= 0 {
		window = DefaultDedupWindow
	}
	return &Deduper{
		capacity: capacity,
		window:   window,
		now:      time.Now,
		seen:     make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Seen records key and reports whether it was already recorded. An empty key
// is never recorded and always reports false, so an event without an id is
// delivered rather than collapsed with every other id-less event.
func (d *Deduper) Seen(key string) bool {
	if key == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.expire(now)
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = d.order.PushBack(seenEntry{key: key, at: now})
	for d.order.Len() > d.capacity {
		d.remove(d.order.Front())
	}
	return false
}

// Forget removes key, so its next sighting is delivered. Receiver uses it when
// it could not hand an event to the application, so the server's retry gets
// through.
func (d *Deduper) Forget(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.seen[key]; ok {
		d.remove(el)
	}
}

func (d *Deduper) expire(now time.Time) {
	for el := d.order.Front(); el != nil; el = d.order.Front() {
		if now.Sub(el.Value.(seenEntry).at) < d.window {
			return
		}
		d.remove(el)
	}
}

func (d *Deduper) remove(el *list.Element) {
	delete(d.seen, el.Value.(seenEntry).key)
	d.order.Remove(el)
}

// dedupFor resolves the Deduper a delivery loop uses: the caller's shared one,
// a private one by default, or none when the caller opted out.
func dedupFor(shared *Deduper, allowDuplicates bool) *Deduper {
	switch {
	case allowDuplicates:
		return nil
	case shared != nil:
		return shared
	default:
		return NewDeduper(0, 0)
	}
}

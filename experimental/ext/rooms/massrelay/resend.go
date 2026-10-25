package massrelay

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"
)

// The relay answers an action over its per-connection rate with
// ErrorEvent{RATE_LIMITED} naming the action's id, and drops it before anyone
// sees it, so sending it again can't deliver it twice. Its limit is per
// second, so the first resend waits a second.
const (
	maxResends = 3
	resendBase = time.Second
	keepSent   = 256
)

// resender stamps an action id on each action a dialled membership sends,
// keeps the most recent, and sends again one the relay reports it dropped
// for the rate (#1621). The embedded transport never sees RATE_LIMITED, since
// only the relay's WebSocket stream sends it.
type resender struct {
	ctx     context.Context
	deliver func(context.Context, *pb.CollabAction) error

	mu    sync.Mutex
	seq   uint64
	sent  map[string]*sentAction
	order []string
}

type sentAction struct {
	action   *pb.CollabAction
	attempts int
}

func newResender(ctx context.Context, deliver func(context.Context, *pb.CollabAction) error) *resender {
	return &resender{ctx: ctx, deliver: deliver, sent: make(map[string]*sentAction)}
}

func (r *resender) send(ctx context.Context, action *pb.CollabAction) error {
	r.mu.Lock()
	r.seq++
	action.ActionId = "d" + strconv.FormatUint(r.seq, 10)
	r.sent[action.ActionId] = &sentAction{action: action}
	r.order = append(r.order, action.ActionId)
	if len(r.order) > keepSent {
		delete(r.sent, r.order[0])
		r.order = r.order[1:]
	}
	r.mu.Unlock()
	return r.deliver(ctx, action)
}

// rateLimited handles the message of a RATE_LIMITED error. An action it no
// longer keeps, or one dropped maxResends+1 times, is given up on.
func (r *resender) rateLimited(message string) {
	id, ok := droppedAction(message)
	if !ok {
		return
	}
	r.mu.Lock()
	s, ok := r.sent[id]
	if ok && s.attempts >= maxResends {
		delete(r.sent, id)
		r.mu.Unlock()
		slog.Warn("massrelay: the relay dropped a message for the rate too many times; giving up on it", "action", id, "tries", maxResends+1)
		return
	}
	var wait time.Duration
	if ok {
		wait = resendBase << s.attempts
		s.attempts++
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	time.AfterFunc(wait, func() {
		if r.ctx.Err() == nil {
			_ = r.deliver(r.ctx, s.action)
		}
	})
}

var droppedRE = regexp.MustCompile(`^Dropped action ("(?:[^"\\]|\\.)*")`)

// droppedAction reads the action id out of a RATE_LIMITED message, as in
// `Dropped action "d12": over 30 messages/sec`.
func droppedAction(message string) (string, bool) {
	m := droppedRE.FindStringSubmatch(message)
	if m == nil {
		return "", false
	}
	id, err := strconv.Unquote(m[1])
	return id, err == nil
}

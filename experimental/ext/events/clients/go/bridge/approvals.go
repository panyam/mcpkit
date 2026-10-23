package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// DefaultApprovalTimeout is how long a PendingApprover waits for an answer
// before denying.
const DefaultApprovalTimeout = 15 * time.Minute

// ErrNoApproval is what Decide returns for an id that is not pending: it was
// never asked, already decided, or timed out.
var ErrNoApproval = errors.New("no pending approval with that id")

// Approval is one run's request for outward-facing tools, waiting on a person.
//
// PayloadUntrusted is the head of the event payload, so the person deciding can
// see what triggered the run. It was written by whoever caused the event, so a
// tool or notifier showing it must present it as data, never as instructions.
type Approval struct {
	ID               string    `json:"id"`
	Event            string    `json:"event"`
	EventID          string    `json:"eventId"`
	SubscriptionID   string    `json:"subscriptionId,omitempty"`
	Instruction      string    `json:"instruction"`
	Tools            []string  `json:"tools"`
	PayloadUntrusted string    `json:"payloadUntrusted,omitempty"`
	RequestedAt      time.Time `json:"requestedAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

// payloadSummaryLen bounds Approval.PayloadUntrusted.
const payloadSummaryLen = 280

// PendingApprover is an Approver that asks a person. Each run that needs
// outward-facing tools is parked as an Approval and waits until someone calls
// Decide, the timeout passes (a denial), or the run's context ends (also a
// denial). The bridge's approvals, approve and deny tools front it when it is
// set as Config.Approvals.
//
// While a run waits, later events for the same watch queue behind it; other
// watches carry on. Nothing about pending approvals is persisted, so a bridge
// that restarts has forgotten them, and the runs that were waiting died with
// the process: their events are redelivered from the watch's cursor and ask
// again.
//
// Safe for concurrent use.
type PendingApprover struct {
	// Timeout is how long a request waits. Zero or negative means
	// DefaultApprovalTimeout.
	Timeout time.Duration

	// OnPending, when set, is told about each new request, e.g. to announce
	// it in the session that created the watch. It runs on its own goroutine,
	// so it may block, and it is not told when the request is decided.
	OnPending func(Approval)

	mu      sync.Mutex
	pending map[string]*pendingApproval
	now     func() time.Time
}

type pendingApproval struct {
	a      Approval
	decide chan bool
}

// Approve implements Approver by waiting for Decide.
func (p *PendingApprover) Approve(ctx context.Context, d eventsclient.Delivery, tools []string) (bool, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	payload := string(d.Event.Data)
	if len(payload) > payloadSummaryLen {
		payload = payload[:payloadSummaryLen] + "..."
	}
	pa := &pendingApproval{
		a: Approval{
			ID:               newApprovalID(),
			Event:            d.QualifiedName(),
			EventID:          d.Event.EventID,
			SubscriptionID:   d.SubscriptionID,
			Instruction:      d.Instruction,
			Tools:            sortedUnique(tools),
			PayloadUntrusted: payload,
			RequestedAt:      now,
			ExpiresAt:        now.Add(timeout),
		},
		decide: make(chan bool, 1),
	}
	p.mu.Lock()
	if p.pending == nil {
		p.pending = make(map[string]*pendingApproval)
	}
	p.pending[pa.a.ID] = pa
	p.mu.Unlock()
	defer p.forget(pa.a.ID)

	if fn := p.OnPending; fn != nil {
		go fn(pa.a)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-pa.decide:
		return ok, nil
	case <-timer.C:
		return false, nil
	case <-ctx.Done():
		return false, nil
	}
}

// Decide answers a pending request. It returns ErrNoApproval when id is not
// pending, which includes a request that timed out a moment ago.
func (p *PendingApprover) Decide(id string, approve bool) error {
	p.mu.Lock()
	pa, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if !ok {
		return ErrNoApproval
	}
	pa.decide <- approve
	return nil
}

// Pending lists the requests waiting now, oldest first.
func (p *PendingApprover) Pending() []Approval {
	p.mu.Lock()
	out := make([]Approval, 0, len(p.pending))
	for _, pa := range p.pending {
		out = append(out, pa.a)
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RequestedAt.Equal(out[j].RequestedAt) {
			return out[i].RequestedAt.Before(out[j].RequestedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (p *PendingApprover) forget(id string) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func newApprovalID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "ap_" + hex.EncodeToString(b[:])
}

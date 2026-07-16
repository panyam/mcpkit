package agent

import (
	"log/slog"
	"sync"
	"time"
)

// MetaKeyTrigger is the vendor _meta key on an events/list EventDef carrying
// a server-suggested trigger binding. Server-advertised triggers are
// suggestions only: the host decides whether to install them, and every
// firing is mediated by TriggerPolicy regardless of origin (the triggers SEP
// draft's stance: the server signals, the host owns the turn).
const MetaKeyTrigger = "io.github.panyam.mcpkit/trigger"

// DefaultTriggerCooldown gates re-arming when a binding does not set its own.
const DefaultTriggerCooldown = 5 * time.Minute

// DefaultTriggerBudget caps proactive firings per policy lifetime when
// unconfigured.
const DefaultTriggerBudget = 10

// TriggerBinding declares one event-to-turn binding.
type TriggerBinding struct {
	// Server and Event select which incoming events match. Empty Server
	// matches any source.
	Server string
	Event  string

	// Filter further narrows matches. Nil matches all occurrences.
	Filter func(IncomingEvent) bool

	// Instructions seed the proactive turn (the trigger's "why you are
	// being invoked" prompt).
	Instructions string

	// Label names the binding in transcripts, logs, and slot state.
	Label string

	// Cooldown is this binding's re-arm floor. Zero means
	// DefaultTriggerCooldown.
	Cooldown time.Duration
}

// TriggerFiring is an approved proactive-turn request: the host runs it
// however it runs turns.
type TriggerFiring struct {
	Binding TriggerBinding
	Event   IncomingEvent
}

// TriggerPolicyConfig assembles a TriggerPolicy.
type TriggerPolicyConfig struct {
	Bindings []TriggerBinding

	// Budget caps total firings for this policy's lifetime (a session,
	// in agentchat's wiring). Zero means DefaultTriggerBudget.
	Budget int

	// Consent, when non-nil, approves each firing after the slot and
	// budget checks. The hook for "ask the user once per binding" UX.
	Consent func(TriggerBinding, IncomingEvent) bool

	// Logger records firings and suppressions (nil discards, per A4).
	Logger *slog.Logger

	// now is injectable for tests; nil means time.Now.
	now func() time.Time
}

// TriggerPolicy is the anti-nag mediation layer between events and
// proactive turns, the CIP playbook's Tier-1 slot machine ported: one slot
// per binding, OPEN to SPENT on fire, re-armed only when BOTH a user
// engagement arrived after the firing AND the binding's cooldown elapsed.
// This is a deterministic approximation of the original's LLM engagement
// signal (positive-engagement detection is a documented future upgrade).
// Safe for concurrent use.
type TriggerPolicy struct {
	cfg TriggerPolicyConfig

	mu       sync.Mutex
	bindings []TriggerBinding
	slots    map[string]*triggerSlot
	firings  int
}

type triggerSlot struct {
	spentAt time.Time
	engaged bool
	spent   bool
}

// NewTriggerPolicy builds the policy.
func NewTriggerPolicy(cfg TriggerPolicyConfig) *TriggerPolicy {
	if cfg.Budget <= 0 {
		cfg.Budget = DefaultTriggerBudget
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	slots := make(map[string]*triggerSlot, len(cfg.Bindings))
	bindings := make([]TriggerBinding, 0, len(cfg.Bindings))
	for _, b := range cfg.Bindings {
		slots[slotKey(b)] = &triggerSlot{}
		bindings = append(bindings, b)
	}
	return &TriggerPolicy{cfg: cfg, bindings: bindings, slots: slots}
}

// Add installs a binding at runtime — the seam a create_trigger meta-tool
// uses so the model can set up a standing behavior through conversation.
// A binding whose slotKey already exists replaces it and resets its slot
// (re-arming the behavior). Safe for concurrent use with OnEvent.
func (t *TriggerPolicy) Add(b TriggerBinding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := slotKey(b)
	if _, exists := t.slots[key]; exists {
		for i := range t.bindings {
			if slotKey(t.bindings[i]) == key {
				t.bindings[i] = b
				break
			}
		}
	} else {
		t.bindings = append(t.bindings, b)
	}
	t.slots[key] = &triggerSlot{}
}

// Remove deletes the binding with the given server/event/label (the slotKey
// components). Unknown bindings are a no-op. Returns whether one was removed.
func (t *TriggerPolicy) Remove(server, event, label string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := slotKey(TriggerBinding{Server: server, Event: event, Label: label})
	if _, exists := t.slots[key]; !exists {
		return false
	}
	delete(t.slots, key)
	for i := range t.bindings {
		if slotKey(t.bindings[i]) == key {
			t.bindings = append(t.bindings[:i], t.bindings[i+1:]...)
			break
		}
	}
	return true
}

// Bindings returns a snapshot of the installed bindings (for list_triggers
// surfaces).
func (t *TriggerPolicy) Bindings() []TriggerBinding {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TriggerBinding(nil), t.bindings...)
}

func slotKey(b TriggerBinding) string { return b.Server + "/" + b.Event + "/" + b.Label }

// OnEvent evaluates ev against every binding and returns at most one
// approved firing (first matching OPEN binding wins), or nil when nothing
// fires. Suppressions (spent slot, budget, consent) are logged, not
// surfaced: servers get no backpressure channel by design.
func (t *TriggerPolicy) OnEvent(ev IncomingEvent) *TriggerFiring {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.cfg.now()

	for _, b := range t.bindings {
		if b.Event != ev.Name || (b.Server != "" && b.Server != ev.Server) {
			continue
		}
		if b.Filter != nil && !b.Filter(ev) {
			continue
		}
		slot := t.slots[slotKey(b)]
		if slot.spent {
			cooldown := b.Cooldown
			if cooldown <= 0 {
				cooldown = DefaultTriggerCooldown
			}
			if !slot.engaged || now.Before(slot.spentAt.Add(cooldown)) {
				t.cfg.Logger.Debug("trigger suppressed: slot spent",
					"label", b.Label, "engaged", slot.engaged)
				continue
			}
			slot.spent = false
		}
		if t.firings >= t.cfg.Budget {
			t.cfg.Logger.Warn("trigger suppressed: budget exhausted",
				"label", b.Label, "budget", t.cfg.Budget)
			return nil
		}
		if t.cfg.Consent != nil && !t.cfg.Consent(b, ev) {
			t.cfg.Logger.Info("trigger suppressed: consent declined", "label", b.Label)
			continue
		}
		slot.spent = true
		slot.spentAt = now
		slot.engaged = false
		t.firings++
		t.cfg.Logger.Info("trigger fired", "label", b.Label, "event", ev.Name, "firings", t.firings)
		return &TriggerFiring{Binding: b, Event: ev}
	}
	return nil
}

// NotifyEngagement records that the user engaged (sent a message) after any
// pending firing: half of every spent slot's re-arm condition, the other
// half being its cooldown.
func (t *TriggerPolicy) NotifyEngagement() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.slots {
		if s.spent {
			s.engaged = true
		}
	}
}

// Firings reports how many proactive turns this policy has approved.
func (t *TriggerPolicy) Firings() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.firings
}

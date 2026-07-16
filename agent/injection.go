package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MetaKeyContextHint is the vendor _meta key on an events/list EventDef that
// carries a ContextHint (see docs/AGENT_DESIGN.md, vendor prefix section).
// Advisory per the context-hints SEP draft: hosts MAY ignore it, and host
// configuration overrides it.
const MetaKeyContextHint = "io.github.panyam.mcpkit/context-hint"

// ContextHint declares how an event's occurrences should reach the model.
// The shape mirrors the context-hints SEP draft one-for-one.
type ContextHint struct {
	// Priority orders injection under the budget: critical, high,
	// medium (default), low.
	Priority string `json:"priority,omitempty"`

	// Aggregate coalesces bursts before injection.
	Aggregate *AggregateHint `json:"aggregate,omitempty"`

	// Template renders the payload as context: {{field}} substitutes
	// top-level payload fields (deliberately no logic; hosts wanting
	// more render themselves). Empty uses the default rendering.
	Template string `json:"template,omitempty"`

	// Retention: "turn" (default; injected once) or "session" (the
	// latest occurrence re-injects every turn until superseded).
	Retention string `json:"retention,omitempty"`

	// Sensitivity: "public" (default), "personal", "restricted".
	// Restricted events are dropped unless the consent gate approves.
	Sensitivity string `json:"sensitivity,omitempty"`
}

// AggregateHint is ContextHint's coalescing sub-shape.
type AggregateHint struct {
	WindowMs int            `json:"windowMs"`
	Strategy WindowStrategy `json:"strategy,omitempty"`
}

// InjectionConfig assembles an InjectionPolicy.
type InjectionConfig struct {
	// Hints maps event name to its ContextHint. Entries here override
	// server-advertised hints (host config wins over vendor _meta).
	Hints map[string]ContextHint

	// Filters and Transforms run before buffering, in order: the
	// developer seam for dropping or rewriting events regardless of
	// hints.
	Filters    []func(IncomingEvent) bool
	Transforms []func(IncomingEvent) (IncomingEvent, bool)

	// Merge is the combiner for merge-strategy windows. Nil uses
	// ShallowMergeJSON.
	Merge func(acc, next IncomingEvent) IncomingEvent

	// MaxPerDrain caps how many rendered entries one Drain returns
	// (highest priority first). Zero means DefaultMaxPerDrain.
	MaxPerDrain int

	// Consent gates sensitive events. Nil allows public and personal
	// and drops restricted; a non-nil gate decides everything except
	// public, which always passes.
	Consent func(hint ContextHint, ev IncomingEvent) bool

	// now is injectable for tests; nil means time.Now.
	now func() time.Time
}

// DefaultMaxPerDrain bounds injected entries per turn when unconfigured.
const DefaultMaxPerDrain = 16

// InjectedContext is one rendered entry ready to join the model's context.
type InjectedContext struct {
	Event    IncomingEvent
	Text     string
	Priority string
}

// InjectionPolicy is the host half of the context-hints SEP draft: it
// buffers incoming events (per-event aggregation windows), renders them, and
// releases them in priority order under a budget when the host drains before
// a turn. Safe for concurrent Ingest against one Drain (the wiring's event
// goroutine vs the turn path).
type InjectionPolicy struct {
	cfg InjectionConfig

	mu      sync.Mutex
	windows map[string]Stage[IncomingEvent]
	pending []IncomingEvent
	session map[string]IncomingEvent
	sessOrd []string
	dropped int
}

// NewInjectionPolicy builds the policy.
func NewInjectionPolicy(cfg InjectionConfig) *InjectionPolicy {
	if cfg.MaxPerDrain <= 0 {
		cfg.MaxPerDrain = DefaultMaxPerDrain
	}
	if cfg.Merge == nil {
		cfg.Merge = ShallowMergeJSON
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &InjectionPolicy{cfg: cfg, windows: map[string]Stage[IncomingEvent]{}, session: map[string]IncomingEvent{}}
}

// HintFromMeta extracts a server-advertised ContextHint from an events/list
// EventDef _meta map. Second return reports presence. Malformed hints are
// ignored (advisory data never breaks the host).
func HintFromMeta(meta map[string]any) (ContextHint, bool) {
	raw, ok := meta[MetaKeyContextHint]
	if !ok {
		return ContextHint{}, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return ContextHint{}, false
	}
	var h ContextHint
	if v, ok := m["priority"].(string); ok {
		h.Priority = v
	}
	if v, ok := m["template"].(string); ok {
		h.Template = v
	}
	if v, ok := m["retention"].(string); ok {
		h.Retention = v
	}
	if v, ok := m["sensitivity"].(string); ok {
		h.Sensitivity = v
	}
	if agg, ok := m["aggregate"].(map[string]any); ok {
		ah := &AggregateHint{}
		if w, ok := agg["windowMs"].(float64); ok {
			ah.WindowMs = int(w)
		}
		if s, ok := agg["strategy"].(string); ok {
			ah.Strategy = WindowStrategy(s)
		}
		h.Aggregate = ah
	}
	return h, true
}

// SetHint installs (or overrides) the hint for an event name at runtime,
// e.g. from events/list discovery. Host-config entries passed at
// construction still win: SetHint is a no-op for names already configured.
func (p *InjectionPolicy) SetHint(name string, h ContextHint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, configured := p.cfg.Hints[name]; configured {
		return
	}
	if p.cfg.Hints == nil {
		p.cfg.Hints = map[string]ContextHint{}
	}
	p.cfg.Hints[name] = h
}

func (p *InjectionPolicy) hint(name string) ContextHint {
	return p.cfg.Hints[name]
}

// Ingest feeds one event through the developer stages and into its
// aggregation window (or straight to pending when the hint has none).
func (p *InjectionPolicy) Ingest(ev IncomingEvent) {
	for _, f := range p.cfg.Filters {
		if !f(ev) {
			return
		}
	}
	for _, tr := range p.cfg.Transforms {
		var keep bool
		if ev, keep = tr(ev); !keep {
			return
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.now()
	h := p.hint(ev.Name)
	if h.Aggregate == nil || h.Aggregate.WindowMs <= 0 {
		p.buffer(ev, h)
		return
	}
	w, ok := p.windows[ev.Name]
	if !ok {
		w = Window(time.Duration(h.Aggregate.WindowMs)*time.Millisecond, h.Aggregate.Strategy,
			func(e IncomingEvent) string { return e.Server + "/" + e.Name },
			p.cfg.Merge)
		p.windows[ev.Name] = w
	}
	for _, released := range w.Push(now, ev) {
		p.buffer(released, h)
	}
}

func (p *InjectionPolicy) buffer(ev IncomingEvent, h ContextHint) {
	if h.Retention == "session" {
		key := ev.Server + "/" + ev.Name
		if _, seen := p.session[key]; !seen {
			p.sessOrd = append(p.sessOrd, key)
		}
		p.session[key] = ev
		return
	}
	p.pending = append(p.pending, ev)
}

// Drain flushes expired windows and returns rendered entries in priority
// order under the budget. Turn-retention entries leave the buffer;
// session-retention entries re-drain every call until superseded. Entries
// beyond the budget stay buffered for the next drain; restricted events
// denied by the consent gate are counted and dropped.
func (p *InjectionPolicy) Drain() []InjectedContext {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.now()

	for name, w := range p.windows {
		for _, released := range w.Flush(now) {
			p.buffer(released, p.hint(name))
		}
	}

	type cand struct {
		ev      IncomingEvent
		session bool
	}
	cands := make([]cand, 0, len(p.pending)+len(p.session))
	for _, key := range p.sessOrd {
		cands = append(cands, cand{ev: p.session[key], session: true})
	}
	for _, ev := range p.pending {
		cands = append(cands, cand{ev: ev})
	}

	// Consent-gate first, then order by priority, THEN apply the budget:
	// the budget must never be consumed by low-priority arrivals while
	// higher-priority entries wait.
	type ranked struct {
		cand
		priority string
	}
	rankedCands := make([]ranked, 0, len(cands))
	for _, c := range cands {
		h := p.hint(c.ev.Name)
		if !p.consentLocked(h, c.ev) {
			p.dropped++
			continue
		}
		rankedCands = append(rankedCands, ranked{cand: c, priority: priorityOrDefault(h.Priority)})
	}
	sort.SliceStable(rankedCands, func(i, j int) bool {
		return priorityRank(rankedCands[i].priority) < priorityRank(rankedCands[j].priority)
	})

	var out []InjectedContext
	p.pending = p.pending[:0]
	for _, rc := range rankedCands {
		if len(out) >= p.cfg.MaxPerDrain {
			if !rc.session {
				p.pending = append(p.pending, rc.ev)
			}
			continue
		}
		out = append(out, InjectedContext{Event: rc.ev, Text: renderEvent(rc.ev, p.hint(rc.ev.Name)), Priority: rc.priority})
	}
	return out
}

// Dropped reports how many events the consent gate has refused so far.
func (p *InjectionPolicy) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *InjectionPolicy) consentLocked(h ContextHint, ev IncomingEvent) bool {
	sensitivity := h.Sensitivity
	if sensitivity == "" || sensitivity == "public" {
		return true
	}
	if p.cfg.Consent != nil {
		return p.cfg.Consent(h, ev)
	}
	return sensitivity == "personal"
}

func priorityOrDefault(p string) string {
	if p == "" {
		return "medium"
	}
	return p
}

func priorityRank(p string) int {
	switch p {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	default:
		return 3
	}
}

var templateField = regexp.MustCompile(`\{\{([a-zA-Z0-9_.-]+)\}\}`)

// renderEvent renders one event as context text: the hint template with
// {{field}} substitution from top-level payload fields, or the default
// "event <name> from <server>: <payload>" line.
func renderEvent(ev IncomingEvent, h ContextHint) string {
	if h.Template == "" {
		return fmt.Sprintf("event %s from %s: %s", ev.Name, ev.Server, strings.TrimSpace(string(ev.Data.Raw())))
	}
	return templateField.ReplaceAllStringFunc(h.Template, func(m string) string {
		field := templateField.FindStringSubmatch(m)[1]
		v, ok := ev.Data.Field(field)
		if !ok {
			return m
		}
		return strings.Trim(string(v.Raw()), `"`)
	})
}

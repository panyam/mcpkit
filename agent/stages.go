package agent

import (
	"encoding/json"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Stage is one step of an event pipeline, generic over the event type so the
// machinery is promotable to any consumer (webhook receivers, page bridges)
// unchanged: nothing here knows about MCP or the model. Push feeds one event
// and returns whatever the stage releases now; Flush releases anything whose
// window has expired by now. Stateless stages return their result from Push
// and nil from Flush. Stages are not safe for concurrent use; drive each
// pipeline from one goroutine (the policies do).
type Stage[E any] interface {
	Push(now time.Time, ev E) []E
	Flush(now time.Time) []E
}

// Pipeline chains stages: each event Pushed cascades through every stage in
// order, and Flush cascades expirations the same way.
type Pipeline[E any] struct {
	stages []Stage[E]
}

// NewPipeline builds a pipeline from stages, applied in order.
func NewPipeline[E any](stages ...Stage[E]) *Pipeline[E] {
	return &Pipeline[E]{stages: stages}
}

// Push cascades ev through the stages.
func (p *Pipeline[E]) Push(now time.Time, ev E) []E {
	events := []E{ev}
	for _, s := range p.stages {
		var next []E
		for _, e := range events {
			next = append(next, s.Push(now, e)...)
		}
		events = next
		if len(events) == 0 {
			return nil
		}
	}
	return events
}

// Flush cascades window expirations: stage i's flushed events still traverse
// stages i+1..n before release.
func (p *Pipeline[E]) Flush(now time.Time) []E {
	var out []E
	for i, s := range p.stages {
		for _, e := range s.Flush(now) {
			events := []E{e}
			for _, later := range p.stages[i+1:] {
				var next []E
				for _, ev := range events {
					next = append(next, later.Push(now, ev)...)
				}
				events = next
			}
			out = append(out, events...)
		}
	}
	return out
}

// Filter keeps events for which fn returns true.
func Filter[E any](fn func(E) bool) Stage[E] { return filterStage[E]{fn: fn} }

type filterStage[E any] struct{ fn func(E) bool }

func (s filterStage[E]) Push(_ time.Time, ev E) []E {
	if s.fn(ev) {
		return []E{ev}
	}
	return nil
}
func (s filterStage[E]) Flush(time.Time) []E { return nil }

// Transform rewrites events; returning false drops the event.
func Transform[E any](fn func(E) (E, bool)) Stage[E] { return transformStage[E]{fn: fn} }

type transformStage[E any] struct{ fn func(E) (E, bool) }

func (s transformStage[E]) Push(_ time.Time, ev E) []E {
	if out, keep := s.fn(ev); keep {
		return []E{out}
	}
	return nil
}
func (s transformStage[E]) Flush(time.Time) []E { return nil }

// WindowStrategy selects how a Window coalesces a burst.
type WindowStrategy string

// Window strategies, matching the context-hint vocabulary: last-wins keeps
// only the newest event per key, merge folds the burst through a combiner,
// debounce releases only after the key has been quiet for the window.
const (
	WindowLastWins WindowStrategy = "last-wins"
	WindowMerge    WindowStrategy = "merge"
	WindowDebounce WindowStrategy = "debounce"
)

// Window buffers events per key and releases per strategy when the window
// expires. For last-wins and merge the window opens at the FIRST event of a
// burst (bounded latency); for debounce every event restarts the window
// (quiet-gap semantics). merge folds with the supplied combiner; nil merge
// falls back to last-wins behavior.
func Window[E any](d time.Duration, strategy WindowStrategy, key func(E) string, merge func(acc, next E) E) Stage[E] {
	return &windowStage[E]{d: d, strategy: strategy, key: key, merge: merge, buckets: map[string]*bucket[E]{}}
}

type bucket[E any] struct {
	acc      E
	deadline time.Time
}

type windowStage[E any] struct {
	d        time.Duration
	strategy WindowStrategy
	key      func(E) string
	merge    func(acc, next E) E
	buckets  map[string]*bucket[E]
	order    []string
}

func (s *windowStage[E]) Push(now time.Time, ev E) []E {
	k := s.key(ev)
	b, ok := s.buckets[k]
	if !ok {
		b = &bucket[E]{acc: ev, deadline: now.Add(s.d)}
		s.buckets[k] = b
		s.order = append(s.order, k)
		return nil
	}
	if s.strategy == WindowMerge && s.merge != nil {
		b.acc = s.merge(b.acc, ev)
	} else {
		b.acc = ev
	}
	if s.strategy == WindowDebounce {
		b.deadline = now.Add(s.d)
	}
	return nil
}

func (s *windowStage[E]) Flush(now time.Time) []E {
	var out []E
	remaining := s.order[:0]
	for _, k := range s.order {
		b := s.buckets[k]
		if !now.Before(b.deadline) {
			out = append(out, b.acc)
			delete(s.buckets, k)
			continue
		}
		remaining = append(remaining, k)
	}
	s.order = remaining
	return out
}

// TypedFilter adapts a payload-typed predicate onto IncomingEvent: the
// payload is Bound once; events whose payload does not decode as T do not
// match. Use for filters that inspect fields with compile-time safety.
func TypedFilter[T any](fn func(T) bool) func(IncomingEvent) bool {
	return func(ev IncomingEvent) bool {
		var v T
		if err := ev.Data.Bind(&v); err != nil {
			return false
		}
		return fn(v)
	}
}

// TypedTransform adapts a payload-typed rewrite onto IncomingEvent; the
// returned payload is re-marshaled into Data. Undecodable payloads pass
// through unchanged (a transform must not silently eat events it cannot
// read).
func TypedTransform[T any](fn func(T) (T, bool)) func(IncomingEvent) (IncomingEvent, bool) {
	return func(ev IncomingEvent) (IncomingEvent, bool) {
		var v T
		if err := ev.Data.Bind(&v); err != nil {
			return ev, true
		}
		out, keep := fn(v)
		if !keep {
			return ev, false
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return ev, true
		}
		ev.Data = core.NewRawJSON(raw)
		return ev, true
	}
}

// MergeWith adapts a payload-typed combiner into an IncomingEvent merge for
// Window's merge strategy: both payloads are Bound as T, folded, and the
// result re-marshaled onto the newer event's envelope. If either payload
// does not decode, the newer event wins (never fabricate data).
func MergeWith[T any](merge func(acc, next T) T) func(acc, next IncomingEvent) IncomingEvent {
	return func(acc, next IncomingEvent) IncomingEvent {
		var a, n T
		if acc.Data.Bind(&a) != nil || next.Data.Bind(&n) != nil {
			return next
		}
		raw, err := json.Marshal(merge(a, n))
		if err != nil {
			return next
		}
		next.Data = core.NewRawJSON(raw)
		return next
	}
}

// ShallowMergeJSON is the default combiner for merge windows on
// IncomingEvent when no typed combiner is supplied: a shallow JSON-object
// merge where the newer event's fields win. Non-object payloads fall back
// to last-wins.
func ShallowMergeJSON(acc, next IncomingEvent) IncomingEvent {
	var a, n map[string]json.RawMessage
	if acc.Data.Bind(&a) != nil || next.Data.Bind(&n) != nil {
		return next
	}
	for k, v := range n {
		a[k] = v
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return next
	}
	next.Data = core.NewRawJSON(raw)
	return next
}

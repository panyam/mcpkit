package core

import (
	"encoding/json"
	"sync"
)

// RawJSON wraps a JSON-RPC raw value (params, result, _meta, ...) with a
// parse-once cache and typed helpers. It replaces bare json.RawMessage at read
// sites so callers stop re-implementing json.Unmarshal plumbing and repeated
// middleware reads share a single parse of the bytes (issue 733).
//
// It is wire-transparent: MarshalJSON returns exactly the bytes it holds and
// UnmarshalJSON captures them, so RawJSON round-trips identically to
// json.RawMessage and preserves the opaque-forwarding property the JSON-RPC
// envelope relies on.
//
// Typed decode is the intended primary path:
//
//	var in MyInput
//	req.Params.Bind(&in)          // whole value into a known shape (handler path)
//	meta, ok := req.Params.Meta() // the _meta sub-object, parsed once
//	meta.Bind(&myMeta)
//
// Field is a dynamic escape hatch for genuinely-dynamic keys (e.g. _meta
// extension fields nobody has a struct for) — prefer a typed Bind where a
// struct exists; string-keyed access trades away compile-time safety.
//
// The zero value is a usable empty/absent value. Construct from bytes with
// NewRawJSON. The lazy spine is built on first Field/Meta call and cached.
// Reads on a RawJSON constructed via NewRawJSON or decoded via UnmarshalJSON
// (both allocate the lazy holder up front) are safe for concurrent goroutines —
// the cache is guarded by sync.Once. The one unguarded case is the zero value's
// first lazy initialization; construct via NewRawJSON before sharing a value
// across goroutines.
type RawJSON struct {
	raw  json.RawMessage
	lazy *rawJSONLazy
}

type rawJSONLazy struct {
	once  sync.Once
	spine map[string]json.RawMessage
	err   error

	// _meta is cached separately from the spine: Meta extracts only the
	// _meta bytes so it never copies a large sibling `arguments` value, which
	// the spine (map[string]json.RawMessage) would. See Meta.
	metaOnce sync.Once
	meta     RawJSON
	metaOK   bool
}

func (m *RawJSON) ensureLazy() *rawJSONLazy {
	if m.lazy == nil {
		m.lazy = &rawJSONLazy{}
	}
	return m.lazy
}

// NewRawJSON wraps raw bytes. The bytes are not copied; callers must not mutate
// them afterward (same contract as json.RawMessage).
func NewRawJSON(raw json.RawMessage) RawJSON {
	return RawJSON{raw: raw, lazy: &rawJSONLazy{}}
}

// MarshalRawJSON marshals v to JSON and wraps the result as a RawJSON —
// convenience for assigning a typed value to a Request.Params field.
func MarshalRawJSON(v any) (RawJSON, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return RawJSON{}, err
	}
	return NewRawJSON(b), nil
}

// object parses and caches the top-level JSON object once. Returns a nil spine
// (no error) for an absent value, and an error when the value is not a JSON
// object — Field/Meta treat both as "no field".
func (m *RawJSON) object() (map[string]json.RawMessage, error) {
	lz := m.ensureLazy()
	lz.once.Do(func() {
		if len(m.raw) == 0 {
			return
		}
		lz.err = json.Unmarshal(m.raw, &lz.spine)
	})
	return lz.spine, lz.err
}

// Field returns the top-level value for key as a sub-RawJSON. ok is false when
// key is absent or the value is not a JSON object. The top-level spine is
// parsed once and cached, so a large sibling value (e.g. a multi-MB
// `arguments`) is scanned only once and never generically materialized — only
// the returned sub-value is parsed, and only if the caller reads it.
func (m *RawJSON) Field(key string) (RawJSON, bool) {
	obj, err := m.object()
	if err != nil || obj == nil {
		return RawJSON{}, false
	}
	v, ok := obj[key]
	if !ok {
		return RawJSON{}, false
	}
	return RawJSON{raw: v, lazy: &rawJSONLazy{}}, true
}

// Meta returns the `_meta` object as a sub-RawJSON, ok=false when absent, null,
// or non-object — the dominant metadata-reader pattern (SEP-414 / 2575 / 2243 /
// 2322).
//
// Unlike Field, Meta does NOT build the full spine: it extracts only the
// `_meta` bytes via a targeted probe, so it scans the value once but never
// copies a large sibling `arguments` blob (which the spine's
// map[string]json.RawMessage would). The result is cached, so many readers on
// one message share a single scan. This keeps the common metadata-only path —
// including the SEP-2575 stateless gate that runs on every request — as cheap
// as a hand-rolled `_meta` probe, plus caching.
func (m *RawJSON) Meta() (RawJSON, bool) {
	lz := m.ensureLazy()
	lz.metaOnce.Do(func() {
		if len(m.raw) == 0 {
			return
		}
		var probe struct {
			Meta json.RawMessage `json:"_meta"`
		}
		if err := json.Unmarshal(m.raw, &probe); err != nil {
			return
		}
		if len(probe.Meta) == 0 || string(probe.Meta) == "null" {
			return
		}
		lz.meta = NewRawJSON(probe.Meta)
		lz.metaOK = true
	})
	return lz.meta, lz.metaOK
}

// Bind decodes the whole raw value into v — the typed / handler path. A nil or
// empty value is a no-op that leaves v unchanged (an absent params does not
// error).
func (m RawJSON) Bind(v any) error {
	if len(m.raw) == 0 {
		return nil
	}
	return json.Unmarshal(m.raw, v)
}

// Raw returns the underlying bytes as a view — do NOT mutate. Empty (nil) for
// an absent value.
func (m RawJSON) Raw() json.RawMessage { return m.raw }

// Len is the byte length of the raw value, O(1). Useful for a transport-level
// max-size guard; not used for any internal branching.
func (m RawJSON) Len() int { return len(m.raw) }

// MarshalJSON returns the raw bytes verbatim (wire-transparent), "null" when
// empty — matching json.RawMessage.
func (m RawJSON) MarshalJSON() ([]byte, error) {
	if len(m.raw) == 0 {
		return []byte("null"), nil
	}
	return m.raw, nil
}

// UnmarshalJSON captures a copy of b (the json package may reuse the slice) and
// resets the parse cache.
func (m *RawJSON) UnmarshalJSON(b []byte) error {
	m.raw = append(m.raw[:0], b...)
	m.lazy = &rawJSONLazy{}
	return nil
}

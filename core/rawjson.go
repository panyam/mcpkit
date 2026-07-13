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
// NewRawJSON. The lazy spine is built on first Field/Meta call and cached;
// reads on a single RawJSON are not yet safe for concurrent goroutines (the
// dispatch/middleware chain reads sequentially per request) — that hardens
// when Request.Params flips to RawJSON (issue 733 slice 3).
type RawJSON struct {
	raw  json.RawMessage
	lazy *rawJSONLazy
}

type rawJSONLazy struct {
	once  sync.Once
	spine map[string]json.RawMessage
	err   error
}

// NewRawJSON wraps raw bytes. The bytes are not copied; callers must not mutate
// them afterward (same contract as json.RawMessage).
func NewRawJSON(raw json.RawMessage) RawJSON {
	return RawJSON{raw: raw, lazy: &rawJSONLazy{}}
}

// object parses and caches the top-level JSON object once. Returns a nil spine
// (no error) for an absent value, and an error when the value is not a JSON
// object — Field/Meta treat both as "no field".
func (m *RawJSON) object() (map[string]json.RawMessage, error) {
	if m.lazy == nil {
		m.lazy = &rawJSONLazy{}
	}
	m.lazy.once.Do(func() {
		if len(m.raw) == 0 {
			return
		}
		m.lazy.err = json.Unmarshal(m.raw, &m.lazy.spine)
	})
	return m.lazy.spine, m.lazy.err
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

// Meta returns the `_meta` object as a sub-RawJSON — sugar for Field("_meta"),
// the dominant metadata-reader pattern (SEP-414 / 2575 / 2243 / 2322).
func (m *RawJSON) Meta() (RawJSON, bool) { return m.Field("_meta") }

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

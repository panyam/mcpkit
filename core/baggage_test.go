package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// --- ExtractBaggage / InjectBaggage round-trip ------------------------------

func TestBaggage_ExtractInject_RoundTrip(t *testing.T) {
	meta := map[string]any{}
	in := core.Baggage("userId=alice,tenant=acme")
	core.InjectBaggage(meta, in)

	if got, _ := meta[core.MetaKeyBaggage].(string); got != string(in) {
		t.Fatalf("InjectBaggage stored %q, expected %q", got, in)
	}
	if got := core.ExtractBaggage(meta); got != in {
		t.Fatalf("round-trip: ExtractBaggage returned %q, expected %q", got, in)
	}
}

func TestBaggage_ExtractBaggage_AbsentReturnsZero(t *testing.T) {
	if got := core.ExtractBaggage(nil); !got.IsZero() {
		t.Fatalf("nil meta must return zero; got %q", got)
	}
	if got := core.ExtractBaggage(map[string]any{}); !got.IsZero() {
		t.Fatalf("empty meta must return zero; got %q", got)
	}
	if got := core.ExtractBaggage(map[string]any{"other": "value"}); !got.IsZero() {
		t.Fatalf("meta without baggage key must return zero; got %q", got)
	}
}

func TestBaggage_ExtractBaggage_NonStringReturnsZero(t *testing.T) {
	meta := map[string]any{core.MetaKeyBaggage: 123}
	if got := core.ExtractBaggage(meta); !got.IsZero() {
		t.Fatalf("non-string baggage value must return zero; got %q", got)
	}
}

func TestBaggage_InjectBaggage_ZeroValueLeavesMetaClean(t *testing.T) {
	meta := map[string]any{}
	core.InjectBaggage(meta, "")
	if _, exists := meta[core.MetaKeyBaggage]; exists {
		t.Fatalf("zero Baggage must NOT write to meta — InjectBaggage left %v", meta)
	}
}

// --- structural validation --------------------------------------------------

func TestBaggage_RejectsControlCharacters(t *testing.T) {
	// CRLF injection is the headline risk — protects HTTPForwardTransport
	// from forwarding a baggage value that would break out into a
	// second HTTP header when stamped on an outbound request.
	meta := map[string]any{core.MetaKeyBaggage: "userId=alice\r\nX-Injected: evil"}
	if got := core.ExtractBaggage(meta); !got.IsZero() {
		t.Fatalf("baggage with control chars must be dropped; got %q", got)
	}
}

func TestBaggage_RejectsHighBitCharacters(t *testing.T) {
	// W3C Baggage values are restricted to ASCII; high-bit bytes
	// indicate malformed or hostile input.
	meta := map[string]any{core.MetaKeyBaggage: "userId=alice,\xff"}
	if got := core.ExtractBaggage(meta); !got.IsZero() {
		t.Fatalf("baggage with high-bit chars must be dropped; got %q", got)
	}
}

func TestBaggage_RejectsOversizedValue(t *testing.T) {
	// SEP-2028 §"Security Implications" — amplification. The 8KB cap
	// is a defensive choice; an input one byte over the cap must be
	// silently dropped.
	huge := "k=" + strings.Repeat("v", 8200)
	meta := map[string]any{core.MetaKeyBaggage: huge}
	if got := core.ExtractBaggage(meta); !got.IsZero() {
		t.Fatalf("baggage exceeding 8KB cap must be dropped; got %d-byte result", len(got))
	}
}

func TestBaggage_AcceptsCommonW3CForms(t *testing.T) {
	cases := []string{
		"userId=alice",
		"userId=alice,tenant=acme",
		"key1=value1;property1=propvalue,key2=value2",
		"k=v",
	}
	for _, c := range cases {
		meta := map[string]any{core.MetaKeyBaggage: c}
		if got := core.ExtractBaggage(meta); got.IsZero() {
			t.Fatalf("valid baggage %q must extract; got zero", c)
		}
	}
}

// --- ExtractBaggageFromParams ------------------------------------------------

func TestBaggage_ExtractFromParams_HappyPath(t *testing.T) {
	params := json.RawMessage(`{"name":"echo","_meta":{"baggage":"userId=alice"}}`)
	if got := core.ExtractBaggageFromParams(params); got != "userId=alice" {
		t.Fatalf("ExtractBaggageFromParams returned %q, expected %q", got, "userId=alice")
	}
}

func TestBaggage_ExtractFromParams_AbsentMetaReturnsZero(t *testing.T) {
	params := json.RawMessage(`{"name":"echo"}`)
	if got := core.ExtractBaggageFromParams(params); !got.IsZero() {
		t.Fatalf("params without _meta must return zero; got %q", got)
	}
}

func TestBaggage_ExtractFromParams_MalformedJSONReturnsZero(t *testing.T) {
	if got := core.ExtractBaggageFromParams(json.RawMessage(`not valid json`)); !got.IsZero() {
		t.Fatalf("malformed params must return zero; got %q", got)
	}
}

func TestBaggage_ExtractFromParams_NilParamsReturnsZero(t *testing.T) {
	if got := core.ExtractBaggageFromParams(nil); !got.IsZero() {
		t.Fatalf("nil params must return zero; got %q", got)
	}
}

// --- InjectBaggageIntoParams -------------------------------------------------

func TestBaggage_InjectIntoParams_NilParams(t *testing.T) {
	got := core.InjectBaggageIntoParams(nil, "userId=alice")
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("nil params + non-zero baggage must return a fresh map; got %T", got)
	}
	meta, _ := obj["_meta"].(map[string]any)
	if v, _ := meta[core.MetaKeyBaggage].(string); v != "userId=alice" {
		t.Fatalf("InjectBaggageIntoParams did not stamp baggage; got %v", obj)
	}
}

func TestBaggage_InjectIntoParams_ExistingObjectAddsMeta(t *testing.T) {
	params := map[string]any{"name": "echo"}
	got := core.InjectBaggageIntoParams(params, "userId=alice")
	obj := got.(map[string]any)
	meta := obj["_meta"].(map[string]any)
	if v, _ := meta[core.MetaKeyBaggage].(string); v != "userId=alice" {
		t.Fatalf("expected baggage to be added; got %v", obj)
	}
	if obj["name"] != "echo" {
		t.Fatalf("expected existing fields preserved; got %v", obj)
	}
}

func TestBaggage_InjectIntoParams_CallerSetValueWins(t *testing.T) {
	params := map[string]any{
		"name":  "echo",
		"_meta": map[string]any{core.MetaKeyBaggage: "userId=caller"},
	}
	got := core.InjectBaggageIntoParams(params, "userId=middleware")
	obj := got.(map[string]any)
	meta := obj["_meta"].(map[string]any)
	if v, _ := meta[core.MetaKeyBaggage].(string); v != "userId=caller" {
		t.Fatalf("caller-set baggage must win over injected value; got %q", v)
	}
}

func TestBaggage_InjectIntoParams_ZeroBaggagePassesThrough(t *testing.T) {
	params := map[string]any{"name": "echo"}
	got := core.InjectBaggageIntoParams(params, "")
	if _, isObj := got.(map[string]any); !isObj {
		t.Fatalf("zero baggage must return params unchanged")
	}
	if obj := got.(map[string]any); obj["_meta"] != nil {
		t.Fatalf("zero baggage must not add _meta; got %v", obj)
	}
}

func TestBaggage_InjectIntoParams_NonObjectParamsUnchanged(t *testing.T) {
	// Positional params (JSON array) — JSON-RPC permits them, but
	// _meta isn't defined inside arrays. Pass through unchanged.
	params := []any{"positional", "args"}
	got := core.InjectBaggageIntoParams(params, "userId=alice")
	arr, ok := got.([]any)
	if !ok {
		t.Fatalf("non-object params must pass through; got %T", got)
	}
	if len(arr) != 2 {
		t.Fatalf("non-object params must not be mutated; got %v", arr)
	}
}

// --- ctx plumbing -----------------------------------------------------------

func TestBaggage_WithBaggage_BaggageFromContext(t *testing.T) {
	ctx := core.WithBaggage(context.Background(), "userId=alice")
	if got := core.BaggageFromContext(ctx); got != "userId=alice" {
		t.Fatalf("BaggageFromContext returned %q, expected %q", got, "userId=alice")
	}
}

func TestBaggage_BaggageFromContext_AbsentReturnsZero(t *testing.T) {
	if got := core.BaggageFromContext(context.Background()); !got.IsZero() {
		t.Fatalf("fresh ctx must return zero baggage; got %q", got)
	}
}

func TestBaggage_WithBaggage_ZeroValueScrubs(t *testing.T) {
	// Storing zero is a valid signal — "tracing was disabled at this
	// boundary." Reading back returns zero, not a fallback.
	ctx := core.WithBaggage(context.Background(), "userId=alice")
	ctx = core.WithBaggage(ctx, "")
	if got := core.BaggageFromContext(ctx); !got.IsZero() {
		t.Fatalf("WithBaggage(zero) must scrub; got %q", got)
	}
}

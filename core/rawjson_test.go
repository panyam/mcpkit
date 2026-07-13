package core

import (
	"encoding/json"
	"testing"
)

func TestRawJSON_FieldAndMeta(t *testing.T) {
	m := NewRawJSON([]byte(`{"name":"greet","arguments":{"x":1},"_meta":{"traceparent":"tp"}}`))

	name, ok := m.Field("name")
	if !ok {
		t.Fatal("name field missing")
	}
	var s string
	if err := name.Bind(&s); err != nil || s != "greet" {
		t.Errorf("name = %q err=%v, want greet", s, err)
	}

	meta, ok := m.Meta()
	if !ok {
		t.Fatal("_meta missing")
	}
	tp, ok := meta.Field("traceparent")
	if !ok {
		t.Fatal("traceparent missing in _meta")
	}
	var tps string
	tp.Bind(&tps)
	if tps != "tp" {
		t.Errorf("traceparent = %q, want tp", tps)
	}

	if _, ok := m.Field("absent"); ok {
		t.Error("absent field should report ok=false")
	}
}

func TestRawJSON_AbsentAndNonObject(t *testing.T) {
	// Absent value: Meta/Field false, Bind no-op.
	var empty RawJSON
	if _, ok := empty.Meta(); ok {
		t.Error("zero-value Meta should be false")
	}
	var v map[string]any
	if err := empty.Bind(&v); err != nil {
		t.Errorf("Bind on empty should be a no-op, got %v", err)
	}
	if v != nil {
		t.Errorf("Bind on empty should leave v unchanged, got %v", v)
	}

	// Non-object raw: Field/Meta false (not a hard error).
	arr := NewRawJSON([]byte(`[1,2,3]`))
	if _, ok := arr.Meta(); ok {
		t.Error("Meta on a JSON array should be false")
	}
	if _, ok := arr.Field("x"); ok {
		t.Error("Field on a JSON array should be false")
	}
	// ...but Bind into the right shape still works.
	var nums []int
	if err := arr.Bind(&nums); err != nil || len(nums) != 3 {
		t.Errorf("Bind array = %v err=%v", nums, err)
	}
}

func TestRawJSON_WireTransparent(t *testing.T) {
	// Marshalling a struct that embeds RawJSON must produce byte-identical
	// output to json.RawMessage, and unmarshalling captures verbatim.
	type env struct {
		Params RawJSON `json:"params"`
	}
	in := []byte(`{"params":{"name":"x","_meta":{"a":1}}}`)
	var e env
	if err := json.Unmarshal(in, &e); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Errorf("round-trip changed bytes:\n in=%s\nout=%s", in, out)
	}

	// Absent params marshals to null, matching json.RawMessage.
	out, _ = json.Marshal(env{})
	if string(out) != `{"params":null}` {
		t.Errorf("empty RawJSON marshaled to %s, want {\"params\":null}", out)
	}
}

func TestRawJSON_ParsesSpineOnce(t *testing.T) {
	// Two Field reads must not re-parse the top level — assert by mutating the
	// raw out from under it after the first read; the cached spine wins.
	m := NewRawJSON([]byte(`{"a":"1","b":"2"}`))
	a, _ := m.Field("a")
	m.raw = []byte(`{"a":"changed"}`) // simulate a re-parse would see different bytes
	b, ok := m.Field("b")             // must still resolve from the cached spine
	if !ok {
		t.Fatal("b should still be found in the cached spine after raw mutation")
	}
	var av, bv string
	a.Bind(&av)
	b.Bind(&bv)
	if av != "1" || bv != "2" {
		t.Errorf("cached spine not used: a=%q b=%q, want 1/2", av, bv)
	}
}

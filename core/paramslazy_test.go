package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRequest_ParamsLazy_SharesCache(t *testing.T) {
	req := &Request{Params: NewRawJSON(json.RawMessage(`{"name":"x","_meta":{"a":1}}`))}
	a := &req.Params
	b := &req.Params
	if a != b {
		t.Fatal("ParamsLazy should return the same cached *RawJSON")
	}
	// Both readers see _meta through the one shared, cached extraction.
	m1, ok1 := a.Meta()
	m2, ok2 := b.Meta()
	if !ok1 || !ok2 {
		t.Fatal("_meta should resolve via the shared cache")
	}
	var v1, v2 map[string]any
	m1.Bind(&v1)
	m2.Bind(&v2)
	if fmt.Sprint(v1) != fmt.Sprint(v2) {
		t.Errorf("shared _meta mismatch: %v vs %v", v1, v2)
	}
}

// TestRawJSON_MetaIsSpineFree asserts Meta resolves the _meta object without
// depending on the top-level spine (Field) — the whole point of the slice-2
// refinement is that a metadata-only reader never materializes a large
// `arguments` sibling.
func TestRawJSON_MetaIsSpineFree(t *testing.T) {
	m := NewRawJSON([]byte(`{"arguments":{"blob":"xxxx"},"_meta":{"protocolVersion":"2025-11-25"}}`))
	meta, ok := m.Meta()
	if !ok {
		t.Fatal("_meta missing")
	}
	var mm map[string]any
	if err := meta.Bind(&mm); err != nil || mm["protocolVersion"] != "2025-11-25" {
		t.Errorf("meta = %v err=%v", mm, err)
	}
	// The spine (lazy.spine) must NOT have been built by a Meta-only access.
	if m.lazy != nil && m.lazy.spine != nil {
		t.Error("Meta() built the spine; it should extract only _meta")
	}
}

func TestRawJSON_MetaAbsentNullNonObject(t *testing.T) {
	cases := map[string]string{
		"absent":     `{"name":"x"}`,
		"null":       `{"_meta":null}`,
		"non-object": `[1,2,3]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m := NewRawJSON([]byte(raw))
			if _, ok := m.Meta(); ok {
				t.Errorf("%s: Meta should report ok=false", name)
			}
		})
	}
}

// BenchmarkDecodeRequestMeta shows the SEP-2575 _meta decode stays allocation-
// light even with a large `arguments` sibling — proof Meta does not copy the
// blob (a spine-based Meta would allocate ~len(arguments) here).
func BenchmarkDecodeRequestMeta(b *testing.B) {
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	for _, sz := range []int{0, 1 << 20} {
		blob := strings.Repeat("x", sz)
		params := json.RawMessage(fmt.Sprintf(`{"name":"echo","arguments":{"blob":%q},%s}`, blob, meta))
		b.Run(fmt.Sprintf("args=%dKB", sz>>10), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m := NewRawJSON(params)
				_, _ = DecodeRequestMetaFromRawJSON(&m)
			}
		})
	}
}

// BenchmarkSharedMetaReaders compares two readers (trace context + SEP-2575
// gate) sharing one &req.Params parse vs each scanning params itself.
func BenchmarkSharedMetaReaders(b *testing.B) {
	meta := `"_meta":{"traceparent":"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01","io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	blob := strings.Repeat("x", 1<<20)
	params := json.RawMessage(fmt.Sprintf(`{"name":"echo","arguments":{"blob":%q},%s}`, blob, meta))

	b.Run("unshared", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = ExtractTraceContextFromParams(params)
			_, _ = DecodeRequestMeta(params)
		}
	})
	b.Run("sharedParamsLazy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := &Request{Params: NewRawJSON(params)}
			_ = ExtractTraceContextFromRawJSON(&req.Params)
			_, _ = DecodeRequestMetaFromRawJSON(&req.Params)
		}
	})
}

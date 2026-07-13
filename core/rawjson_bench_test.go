package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Issue 733 slice 1: the trace middleware reads three _meta fields per
// tools/call (trace context, baggage, tracelink). These benchmarks compare the
// old per-field parse (3 full scans of params) against the shared RawJSON (one
// spine scan), across arguments payload sizes — the win grows with the blob
// that sits next to _meta on the wire.
//
//	go test ./core/ -run '^$' -bench TraceExtract -benchmem

func benchParams(argBytes int) json.RawMessage {
	blob := strings.Repeat("x", argBytes)
	return json.RawMessage(fmt.Sprintf(
		`{"name":"echo","arguments":{"blob":%q},"_meta":{"traceparent":"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}}`,
		blob))
}

func BenchmarkTraceExtract(b *testing.B) {
	for _, sz := range []int{0, 1 << 10, 1 << 20} {
		params := benchParams(sz)

		b.Run(fmt.Sprintf("perField/args=%dKB", sz>>10), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ExtractTraceContextFromParams(params)
				_ = ExtractBaggageFromParams(params)
				_ = ExtractTraceLinkFromParams(params)
			}
		})

		b.Run(fmt.Sprintf("sharedRawJSON/args=%dKB", sz>>10), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m := NewRawJSON(params)
				_ = ExtractTraceContextFromRawJSON(&m)
				_ = ExtractBaggageFromRawJSON(&m)
				_ = ExtractTraceLinkFromRawJSON(&m)
			}
		})
	}
}

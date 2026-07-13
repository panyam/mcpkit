package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// Issue 489: quantify how much of a tools/call dispatch is re-scanning
// req.Params. These benchmarks measure the dispatcher path (handleToolsCall +
// schema validation) across arguments payload sizes, plus the cost of a single
// peek-decode of the same params as the per-scan unit. Run:
//
//	go test ./server/ -run '^$' -bench 'ToolsCallDispatch|SinglePeekDecode' -benchmem

func benchToolsParams(argBytes int) json.RawMessage {
	blob := strings.Repeat("x", argBytes)
	return json.RawMessage(fmt.Sprintf(
		`{"name":"echo","arguments":{"blob":%q},"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25"}}`,
		blob))
}

func benchDispatcher(tb testing.TB, schema bool) *Dispatcher {
	tb.Helper()
	d := NewDispatcher(core.ServerInfo{Name: "b", Version: "1"})
	def := core.ToolDef{Name: "echo", Description: "echo"}
	if schema {
		def.InputSchema = map[string]any{"type": "object"}
	}
	d.RegisterTool(def, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})
	if !schema {
		d.skipSchemaValidation = true
	}
	initDispatcher(d)
	return d
}

var benchSizes = []int{0, 1 << 10, 64 << 10, 1 << 20} // 0, 1KB, 64KB, 1MB

func BenchmarkToolsCallDispatch(b *testing.B) {
	for _, schema := range []bool{false, true} {
		for _, sz := range benchSizes {
			name := fmt.Sprintf("schema=%v/args=%dKB", schema, sz>>10)
			b.Run(name, func(b *testing.B) {
				d := benchDispatcher(b, schema)
				req := &core.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: benchToolsParams(sz)}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = d.Dispatch(context.Background(), req)
				}
			})
		}
	}
}

// BenchmarkSinglePeekDecode is the per-scan unit: one json.Unmarshal of the
// same params into a peek struct (what each middleware site does). Dividing a
// dispatch's cost delta by this gives the effective re-scan count.
func BenchmarkSinglePeekDecode(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(fmt.Sprintf("args=%dKB", sz>>10), func(b *testing.B) {
			params := benchToolsParams(sz)
			var p struct {
				Name string `json:"name"`
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = json.Unmarshal(params, &p)
			}
		})
	}
}

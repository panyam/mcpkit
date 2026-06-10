package gormstore

import (
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// TestEventBufferStore_Conformance runs the conformance matrix from
// the parent package against every backend the gorm sub-module
// supports: in-memory (reference), sqlite (default), and Postgres
// (when MCPKIT_EVENTS_TEST_PGDB is set). All three must agree on the
// seam's behavior.
//
// maxSize=0 (unbounded) for the gorm backend because the gorm impl
// doesn't honor a row-count cap — eviction is TTL-driven via the
// background sweeper. The conformance suite's MaxSizeEvictionMarks
// Truncated subtest auto-skips when maxSize<=0.
func TestEventBufferStore_Conformance(t *testing.T) {
	for _, bk := range backends(t) {
		bk := bk
		t.Run(bk.name, func(t *testing.T) {
			events.RunEventBufferConformance(t, bk.newBufferStore(t), 0)
		})
	}
}

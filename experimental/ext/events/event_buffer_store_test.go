package events

import "testing"

// Tests for the in-memory EventBufferStore impl. Body lives in
// event_buffer_conformance.go (RunEventBufferConformance) so backends
// in sub-modules (stores/gorm) can run the same matrix against their
// own constructors.

func TestInMemoryEventBufferStore_Conformance(t *testing.T) {
	RunEventBufferConformance(t, NewInMemoryEventBufferStore(50), 50)
}

func TestInMemoryEventBufferStore_Unbounded(t *testing.T) {
	RunEventBufferConformance(t, NewInMemoryEventBufferStore(0), 0)
}

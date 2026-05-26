package core

import (
	"encoding/json"
	"testing"
)

// SEP-2575 stateless-wire bridge helpers (NewSamplingInputRequest /
// NewElicitationInputRequest / DecodeSamplingInputResponse /
// DecodeElicitationInputResponse) — round-trip locks the wire shape
// against the legacy ctx.Sample/Elicit method names so the same client
// SamplingHandler / ElicitationHandler accepts both forms.

func TestNewSamplingInputRequest_WireShape(t *testing.T) {
	req := CreateMessageRequest{
		Messages: []SamplingMessage{
			{Role: "user", Content: Content{Type: "text", Text: "summarize this"}},
		},
		MaxTokens: 256,
	}
	got := NewSamplingInputRequest(req)
	if got.Method != "sampling/createMessage" {
		t.Errorf("Method = %q, want sampling/createMessage", got.Method)
	}
	if len(got.Params) == 0 {
		t.Fatal("Params empty; want serialized CreateMessageRequest")
	}
	// Caller-shape round-trip: the bytes must decode back as the
	// same request — this is what the client's SamplingHandler will
	// receive after the round trip.
	var back CreateMessageRequest
	if err := json.Unmarshal(got.Params, &back); err != nil {
		t.Fatalf("Params did not decode: %v", err)
	}
	if back.MaxTokens != 256 || len(back.Messages) != 1 {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestNewElicitationInputRequest_WireShape(t *testing.T) {
	req := ElicitationRequest{
		Message:         "What is your name?",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}
	got := NewElicitationInputRequest(req)
	if got.Method != "elicitation/create" {
		t.Errorf("Method = %q, want elicitation/create", got.Method)
	}
	var back ElicitationRequest
	if err := json.Unmarshal(got.Params, &back); err != nil {
		t.Fatalf("Params did not decode: %v", err)
	}
	if back.Message != "What is your name?" {
		t.Errorf("Message round-trip mismatch: got %q", back.Message)
	}
}

func TestDecodeSamplingInputResponse(t *testing.T) {
	// What the client puts in inputResponses after handling the
	// sampling request: a CreateMessageResult serialized as the
	// opaque entry payload.
	raw := json.RawMessage(`{
		"role": "assistant",
		"content": {"type": "text", "text": "Hello, world."},
		"model": "test-model",
		"stopReason": "endTurn"
	}`)
	got, err := DecodeSamplingInputResponse(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Role != "assistant" || got.Model != "test-model" {
		t.Errorf("decoded mismatch: %+v", got)
	}
}

func TestDecodeElicitationInputResponse(t *testing.T) {
	raw := json.RawMessage(`{"action": "accept", "content": {"name": "Sri"}}`)
	got, err := DecodeElicitationInputResponse(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Action != "accept" {
		t.Errorf("Action = %q, want accept", got.Action)
	}
	name, ok := got.Content["name"].(string)
	if !ok || name != "Sri" {
		t.Errorf("Content.name = %v, want Sri", got.Content)
	}
}

func TestDecode_RejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeSamplingInputResponse(json.RawMessage(`not-json`)); err == nil {
		t.Error("expected error for malformed sampling response, got nil")
	}
	if _, err := DecodeElicitationInputResponse(json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed elicitation response, got nil")
	}
}

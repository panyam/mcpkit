package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SEP-2322 (Multi-Round Tool Result, "MRTR") base types.
//
// MRTR is the ephemeral input-required flow on top of any request method
// (typically tools/call): when a server needs additional input before it
// can produce a final result, it returns InputRequiredResult; the client
// retries the same request method with `inputResponses` keyed against the
// previously-emitted ids (plus the echoed `requestState`). Rounds continue
// until the server returns a complete result, a CreateTaskResult (handing
// off to tasks-v2 in core/task_v2.go), or an error.
//
// The discriminator (ResultType) and the round-token signing helpers
// (Sign/VerifyRequestState, Sign/VerifyMRTRState) live here because
// SEP-2322 defines them. SEP-2663 tasks-v2 consumes the same discriminator
// and the same input-key/request shape, but those types stay owned by
// SEP-2322.

// ResultType is the discriminator on a tools/call response.
//
// "task" indicates a task-based response (CreateTaskResult). The "complete"
// and "input_required" values come from MRTR (Multi-Round Tool Result,
// SEP-2322) and signal whether a tool result is final or expects further
// input rounds. The "input_required" variant was renamed from "incomplete"
// in SEP-2322 commit de6d76fb, merged 2026-05-06.
type ResultType string

const (
	ResultTypeTask          ResultType = "task"
	ResultTypeComplete      ResultType = "complete"       // SEP-2322
	ResultTypeInputRequired ResultType = "input_required" // SEP-2322
)

// InputRequest is a single MRTR input request enqueued by a server during
// tool execution: a method (e.g., "elicitation/create", "sampling/createMessage")
// plus opaque params encoded per that method's request schema. // SEP-2322
type InputRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// InputRequests is the wire-format map from request key to InputRequest.
// Keys are server-chosen identifiers (e.g., "elicit-1") that the client
// echoes verbatim in the matching InputResponses entry. // SEP-2322
type InputRequests = map[string]InputRequest

// InputResponses is the wire-format map from request key to opaque response
// payload. Keys MUST match those previously returned in InputRequests. The
// payload shape is defined by the original request method. // SEP-2322
type InputResponses = map[string]json.RawMessage

// InputRequiredResult is the SEP-2322 wire envelope returned by tools/call
// (and other request methods, in principle) when the server needs additional
// input from the client before it can produce a final result. Clients retry
// the same request with `inputResponses` (and the echoed `requestState`)
// until the server returns a complete result, a CreateTaskResult, or an
// error.
//
// Renamed from IncompleteResult in SEP-2322 commit de6d76fb (merged
// 2026-05-06) per dsp-ant request. The wire `resultType` value flipped
// from "incomplete" to "input_required" at the same time. SEP-2663 had
// not yet adopted the rename as of 2026-05-07 PM, but Caitie committed
// to a coherent Draft Spec + Schema at the 5/15 RC (issue comment
// 4384052694), so the alignment direction is "input_required" both
// places.
//
// Wire-format note: the `resultType` discriminator is camelCase like every
// other MCP wire field — Luca confirmed camelCase is the SEP-2322 spec
// standard. (The upstream conformance suite briefly used snake_case but
// that's being corrected on their side.)
type InputRequiredResult struct {
	// ResultType is always "input_required". Defaulted by MarshalJSON when
	// empty so server handlers can build the struct without thinking about
	// it.
	ResultType ResultType `json:"resultType"`

	// InputRequests is the map of server-chosen request keys → InputRequest
	// describing what the server needs from the client. Keys are echoed back
	// verbatim by the client in the matching InputResponses entry.
	InputRequests InputRequests `json:"inputRequests,omitempty"`

	// RequestState is the opaque session-continuation token. SEP-2322 says
	// servers MUST treat any echoed value as attacker-controlled; the helper
	// API in dispatch HMAC-signs it when a key is configured.
	RequestState string `json:"requestState,omitempty"`
}

// MarshalJSON defaults ResultType to ResultTypeInputRequired so handlers
// that build an InputRequiredResult{} literal don't have to set the
// discriminator.
func (r InputRequiredResult) MarshalJSON() ([]byte, error) {
	type alias InputRequiredResult
	if r.ResultType == "" {
		r.ResultType = ResultTypeInputRequired
	}
	return json.Marshal(alias(r))
}

// SEP-2575 stateless-wire bridge helpers.
//
// The SEP-2575 stateless wire forbids server-initiated JSON-RPC requests
// on a tools/call response stream (the conformance check
// HttpServerNoIndependentRequestsOnStream is explicit). That means
// ctx.Sample / ctx.Elicit's legacy push path cannot be used on the
// stateless wire — handlers must route the request through MRTR
// (SEP-2322) instead: enqueue an InputRequest, return InputRequiredResult,
// let the client retry the same tools/call with the answer in
// inputResponses.
//
// These helpers package the same CreateMessageRequest and ElicitationRequest
// types ctx.Sample/Elicit accept as MRTR-compatible InputRequest values.
// The example fixture in examples/stateless/ demonstrates the pattern
// end-to-end via test_streaming_elicitation.

// NewSamplingInputRequest wraps a CreateMessageRequest as a MRTR
// InputRequest the handler can enqueue under any key in InputRequests.
// The wire method is sampling/createMessage, identical to the legacy
// server-initiated path — client-side dispatch routes either form into
// the same SamplingHandler.
//
// On a marshal failure the returned InputRequest has empty Params; the
// dispatch path will surface that as a client-side validation error so
// the round trip still terminates rather than hanging.
func NewSamplingInputRequest(req CreateMessageRequest) InputRequest {
	raw, _ := MarshalJSON(req)
	return InputRequest{
		Method: "sampling/createMessage",
		Params: raw,
	}
}

// NewElicitationInputRequest wraps an ElicitationRequest as a MRTR
// InputRequest. The wire method is elicitation/create. Symmetric with
// NewSamplingInputRequest above.
//
// Caller is responsible for any SEP-2356 fileInputs schema stripping if
// the calling client may not declare the capability — the legacy
// ctx.Elicit does this transparently; the MRTR path is opt-in so the
// caller stays in control.
func NewElicitationInputRequest(req ElicitationRequest) InputRequest {
	raw, _ := MarshalJSON(req)
	return InputRequest{
		Method: "elicitation/create",
		Params: raw,
	}
}

// DecodeSamplingInputResponse decodes a single inputResponses entry as
// a CreateMessageResult — symmetric with NewSamplingInputRequest above.
// Used on the second tools/call round after the client has answered
// the sampling request.
func DecodeSamplingInputResponse(raw json.RawMessage) (CreateMessageResult, error) {
	var out CreateMessageResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return CreateMessageResult{}, fmt.Errorf("decode sampling response: %w", err)
	}
	return out, nil
}

// DecodeElicitationInputResponse decodes a single inputResponses entry
// as an ElicitationResult.
func DecodeElicitationInputResponse(raw json.RawMessage) (ElicitationResult, error) {
	var out ElicitationResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return ElicitationResult{}, fmt.Errorf("decode elicitation response: %w", err)
	}
	return out, nil
}

// --- MRTR requestState signing ---
//
// SEP-2322's InputRequiredResult.RequestState carries an opaque token from
// server to client which the client MUST echo on subsequent requests. The
// helpers below implement an HMAC-SHA256-signed encoding so a stateless
// server can verify the token came back unmodified, plus a plaintext mode
// for deployments without a signing key.
//
// Wire format (signed): "<base64url-signature>.<base64url-payload>" where
// payload is the JSON encoding of MRTRRoundState {tool, answered, exp}.
// base64url is chosen to keep the token URL/header-safe without escaping.
//
// SignRequestState / VerifyRequestState use the older requestStatePayload
// {taskId, exp} shape. SEP-2663 removed `requestState` from the tasks-v2
// wire, so the tasks-v2 paths no longer call them. They are retained
// because server/mrtr.go reads legacy single-round tokens with this shape
// for backward compatibility; that shim is removable once all in-flight
// rounds have rotated past.

// ErrRequestStateMalformed indicates the encoded requestState couldn't be
// parsed (missing separator, bad base64, bad JSON inside the payload).
var ErrRequestStateMalformed = errors.New("requestState malformed")

// ErrRequestStateInvalidSignature indicates the HMAC signature did not match
// — either tampered payload or wrong key.
var ErrRequestStateInvalidSignature = errors.New("requestState signature invalid")

// ErrRequestStateExpired indicates the payload's expiry is in the past.
var ErrRequestStateExpired = errors.New("requestState expired")

// requestStatePayload is the JSON body wrapped inside a signed
// requestState token (legacy single-round MRTR shape). Compact (two
// fields) so the encoded form stays small.
type requestStatePayload struct {
	TaskID string `json:"taskId"`
	Exp    int64  `json:"exp"` // unix seconds
}

// SignRequestState produces an HMAC-SHA256-signed requestState token for
// the given taskID, valid for ttl. The encoded form is opaque to clients
// and round-trippable via VerifyRequestState. Panics if key is empty.
//
// Retained for backward-compat with legacy MRTR tokens; see the section
// banner above.
func SignRequestState(key []byte, taskID string, ttl time.Duration) string {
	if len(key) == 0 {
		panic("core.SignRequestState: empty key")
	}
	payload := requestStatePayload{
		TaskID: taskID,
		Exp:    time.Now().Add(ttl).Unix(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("core.SignRequestState: marshal payload: %v", err))
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sig) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes)
}

// VerifyRequestState checks an incoming requestState token against the
// signing key and current time. Returns the embedded taskID on success.
// Errors:
//   - ErrRequestStateMalformed: structural parse failures (split, base64, JSON)
//   - ErrRequestStateInvalidSignature: signature mismatch (tampered or wrong key)
//   - ErrRequestStateExpired: payload exp is in the past
//
// Uses hmac.Equal for constant-time signature comparison. Retained for
// backward-compat with legacy MRTR tokens; see the section banner above.
func VerifyRequestState(key []byte, state string) (taskID string, err error) {
	if len(key) == 0 {
		return "", ErrRequestStateMalformed
	}
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		return "", ErrRequestStateMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(state[:dot])
	if err != nil {
		return "", ErrRequestStateMalformed
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(state[dot+1:])
	if err != nil {
		return "", ErrRequestStateMalformed
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return "", ErrRequestStateInvalidSignature
	}
	var payload requestStatePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", ErrRequestStateMalformed
	}
	if payload.Exp > 0 && time.Now().Unix() > payload.Exp {
		return "", ErrRequestStateExpired
	}
	return payload.TaskID, nil
}

// MRTRRoundState is the payload encoded inside an SEP-2322 ephemeral
// requestState token across multi-round flows. The dispatcher uses it to
// carry accumulated InputResponses from previous rounds back to the
// handler — the wire protocol only ships the current round's responses,
// so without this carry-over a stateless handler couldn't see history.
//
// Tool is the tool name the token was issued for; replays against a
// different tool fail with ErrRequestStateInvalidSignature. Answered is
// the merged inputResponses map (raw, opaque per-key payloads). Exp is
// the unix-seconds expiry, mirroring the requestStatePayload semantics.
type MRTRRoundState struct {
	Tool     string                     `json:"tool"`
	Answered map[string]json.RawMessage `json:"answered,omitempty"`
	Exp      int64                      `json:"exp"`
}

// SignMRTRState produces an HMAC-SHA256-signed requestState token wrapping
// state. ttl bounds the token's validity. Same wire format as
// SignRequestState ("<base64url-sig>.<base64url-payload>") so the same
// helpers in transports/middleware that already strip / re-add the prefix
// work uniformly. Panics if key is empty — callers should fall back to
// the plaintext encoder when no signing key is configured.
func SignMRTRState(key []byte, state MRTRRoundState, ttl time.Duration) (string, error) {
	if len(key) == 0 {
		return "", errors.New("core.SignMRTRState: empty key")
	}
	if state.Exp == 0 {
		state.Exp = time.Now().Add(ttl).Unix()
	}
	payloadBytes, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal MRTR state: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sig) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes), nil
}

// VerifyMRTRState validates an incoming MRTR requestState token against the
// signing key + current time and returns the embedded round state. Errors:
//   - ErrRequestStateMalformed: structural parse failures (split, base64, JSON)
//   - ErrRequestStateInvalidSignature: signature mismatch (tampered or wrong key)
//   - ErrRequestStateExpired: payload exp is in the past
//
// Uses hmac.Equal for constant-time signature comparison.
func VerifyMRTRState(key []byte, token string) (MRTRRoundState, error) {
	if len(key) == 0 {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return MRTRRoundState{}, ErrRequestStateInvalidSignature
	}
	var state MRTRRoundState
	if err := json.Unmarshal(payloadBytes, &state); err != nil {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	if state.Exp > 0 && time.Now().Unix() > state.Exp {
		return MRTRRoundState{}, ErrRequestStateExpired
	}
	return state, nil
}

// EncodeMRTRStatePlaintext returns a base64url-encoded JSON of state with no
// integrity guarantee — used by the dispatcher in plaintext mode when no
// signing key is configured. The encoded form is parseable but trivially
// forgeable; SEP-2322 servers without a signing key must treat it as
// untrusted regardless.
func EncodeMRTRStatePlaintext(state MRTRRoundState) (string, error) {
	if state.Exp == 0 {
		state.Exp = time.Now().Add(24 * time.Hour).Unix()
	}
	payloadBytes, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal MRTR state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payloadBytes), nil
}

// DecodeMRTRStatePlaintext parses a token produced by EncodeMRTRStatePlaintext.
// Returns ErrRequestStateMalformed for unparseable tokens and
// ErrRequestStateExpired when the embedded exp is in the past. No signature
// check (plaintext mode has none).
func DecodeMRTRStatePlaintext(token string) (MRTRRoundState, error) {
	payloadBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	var state MRTRRoundState
	if err := json.Unmarshal(payloadBytes, &state); err != nil {
		return MRTRRoundState{}, ErrRequestStateMalformed
	}
	if state.Exp > 0 && time.Now().Unix() > state.Exp {
		return MRTRRoundState{}, ErrRequestStateExpired
	}
	return state, nil
}


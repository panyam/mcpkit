package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2322 MRTR (Multi Round-Trip Requests) — ephemeral, stateless flow.
//
// The server returns IncompleteResult{inputRequests, requestState}; the
// client retries the SAME tools/call with inputResponses + the echoed
// requestState. There is no per-round server-side state — the requestState
// token is the entire round handle, which is why integrity matters.
//
// This file holds the runtime + signing helpers that the tools/call
// dispatch path uses to mint, verify, and reshape MRTR payloads.

// mrtrTokenPrefix is the legacy discriminator that lived in the signed
// payload's taskID slot before Phase 4 promoted MRTR to its own MRTRRoundState
// shape. Kept around for the verifier's backward-compat path so in-flight
// single-round tokens still validate during the rollover. Removable once
// no in-flight tokens predate the Phase 4 deploy.
const mrtrTokenPrefix = "mrtr:"

// mrtrRuntime holds per-server MRTR config. Stateless beyond the signing
// key: ephemeral MRTR is not supposed to keep round state on the server.
type mrtrRuntime struct {
	// signingKey is the optional HMAC-SHA256 key used to sign requestState
	// tokens. nil = plaintext mode: requestState is a random nonce with no
	// integrity guarantee. Production deployments should configure a key
	// via WithMRTRSigning.
	signingKey []byte

	// ttl is how long a signed requestState stays valid. Zero falls back
	// to the core default (24h). No effect in plaintext mode.
	ttl time.Duration
}

// mintRequestState produces a fresh requestState token wrapping the
// accumulated InputResponses for the next MRTR round. When a signing key
// is configured the token is HMAC-signed; otherwise it's a base64url
// JSON blob with no integrity guarantee.
//
// answered is the merged inputResponses map dispatch has accumulated
// across all previous rounds plus the current one (the handler-visible
// snapshot).
//
// Returns "" when encoding fails — dispatch then omits requestState
// entirely on the IncompleteResult, which is spec-legal (the field is
// optional). Failure here is non-fatal because handler progress is more
// important than session continuity.
func (r *mrtrRuntime) mintRequestState(toolName string, answered map[string]json.RawMessage) string {
	state := core.MRTRRoundState{
		Tool:     toolName,
		Answered: answered,
		Exp:      time.Now().Add(r.effectiveTTL()).Unix(),
	}
	if len(r.signingKey) == 0 {
		token, err := core.EncodeMRTRStatePlaintext(state)
		if err != nil {
			return ""
		}
		return token
	}
	token, err := core.SignMRTRState(r.signingKey, state, r.effectiveTTL())
	if err != nil {
		return ""
	}
	return token
}

// verifyRequestState validates a client-echoed requestState and returns
// the accumulated InputResponses encoded inside it. Empty token is allowed
// (first call, or the previous round didn't mint one). Errors:
//   - core.ErrRequestStateMalformed: structurally bad token
//   - core.ErrRequestStateInvalidSignature: signed-mode token failed HMAC
//     OR signed-mode tool name mismatch (token issued for tool A replayed
//     against tool B)
//   - core.ErrRequestStateExpired: embedded exp is past
//
// In plaintext mode any decodable token passes structural checks; the
// embedded tool name still has to match — even without integrity, swapping
// toolnames mid-round is a programmer error worth catching.
func (r *mrtrRuntime) verifyRequestState(token, toolName string) (core.MRTRRoundState, error) {
	if token == "" {
		return core.MRTRRoundState{}, nil
	}
	var state core.MRTRRoundState
	var err error
	if len(r.signingKey) == 0 {
		state, err = core.DecodeMRTRStatePlaintext(token)
	} else {
		state, err = core.VerifyMRTRState(r.signingKey, token)
		// Backward-compat: legacy single-round tokens minted before Phase 4
		// used SignRequestState with a "mrtr:<tool>" payload. Detect them
		// and treat as a state with no answered map. Removable once all
		// in-flight rounds have rotated past.
		if err == core.ErrRequestStateMalformed {
			if id, vErr := core.VerifyRequestState(r.signingKey, token); vErr == nil {
				if strings.HasPrefix(id, mrtrTokenPrefix) {
					trimmed := strings.TrimPrefix(id, mrtrTokenPrefix)
					if trimmed == toolName {
						return core.MRTRRoundState{Tool: trimmed}, nil
					}
					return core.MRTRRoundState{}, core.ErrRequestStateInvalidSignature
				}
			}
		}
	}
	if err != nil {
		return core.MRTRRoundState{}, err
	}
	if state.Tool != "" && state.Tool != toolName {
		return core.MRTRRoundState{}, core.ErrRequestStateInvalidSignature
	}
	return state, nil
}

// mergeInputResponses overlays current round responses onto the accumulated
// map from the previous rounds (carried in requestState). Current responses
// win on key collision so a client correcting an earlier answer can do so
// by re-sending it under the same key.
//
// Returns nil only when both inputs are empty so dispatch can pass the
// nil through to ToolContext (vs an empty allocated map) — the difference
// matters for handlers that branch on `inputResponses == nil` as the
// "first call" signal.
func mergeInputResponses(previous, current map[string]json.RawMessage) map[string]json.RawMessage {
	if len(previous) == 0 && len(current) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(previous)+len(current))
	for k, v := range previous {
		out[k] = v
	}
	for k, v := range current {
		out[k] = v
	}
	return out
}

func (r *mrtrRuntime) effectiveTTL() time.Duration {
	if r.ttl <= 0 {
		return 24 * time.Hour
	}
	return r.ttl
}

// mrtrPlaintextNonce returns a 128-bit base64url-encoded random string for
// use as a plaintext-mode requestState. Random failures are reduced to an
// empty string (the dispatcher then omits requestState entirely on the
// IncompleteResult, which is spec-legal — the field is optional).
func mrtrPlaintextNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// --- tools/call request envelope (MRTR-aware) ---

// toolsCallEnvelope is the MRTR-aware tools/call request shape. The MRTR
// fields (inputResponses, requestState) live alongside arguments at the
// params top level — NOT under arguments — per SEP-2322's wire contract.
//
// progressToken still rides on _meta as before.
type toolsCallEnvelope struct {
	Name           string              `json:"name"`
	Arguments      json.RawMessage     `json:"arguments,omitempty"`
	InputResponses core.InputResponses `json:"inputResponses,omitempty"`
	RequestState   string              `json:"requestState,omitempty"`
	Meta           *toolsCallMeta      `json:"_meta,omitempty"`
}

type toolsCallMeta struct {
	ProgressToken any `json:"progressToken,omitempty"`
}

// --- WithRequestStateSigning option ---

// WithRequestStateSigning configures the HMAC-SHA256 key the server uses
// to sign and verify SEP-2322 requestState tokens. The same key is shared
// by ephemeral MRTR (tools/call IncompleteResult round-trips) and SEP-2663
// Tasks (tasks/get / tasks/update / tasks/cancel) so production deployments
// only configure HMAC once and have all signed-state surfaces work.
//
// Servers without this option run in unsigned mode:
//   - MRTR requestState is a random nonce with no integrity guarantee
//   - Tasks requestState is the bare taskID (legacy plaintext mode)
//
// SEP-2322 / SEP-2663 say servers MUST treat requestState as
// attacker-controlled, so production deployments SHOULD configure a key.
//
// ttl bounds how long a minted token stays valid; zero falls back to 24h.
//
// Per-RegisterTasks override: TasksConfig.RequestStateKey still wins when
// set explicitly, so callers that need separate keys for tasks vs MRTR
// (key rotation, security boundaries) can opt out of the shared default.
func WithRequestStateSigning(key []byte, ttl time.Duration) Option {
	return func(o *serverOptions) {
		o.requestStateKey = append([]byte(nil), key...)
		o.requestStateTTL = ttl
	}
}

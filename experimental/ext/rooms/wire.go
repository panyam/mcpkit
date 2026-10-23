package rooms

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// Methods a page and the bridge exchange beyond MCP's own tools/list,
// tools/call, notifications/tools/list_changed, notifications/cancelled and
// notifications/progress. All are notifications.
const (
	// MethodPageState: page to bridge, {label, focused, focusedAt}. The
	// label is what list_pages shows; focused true stamps the page's
	// last-focus time, at focusedAt (RFC 3339) when given, else on arrival.
	// A page sends focusedAt when it greets a bridge, so a tab its user
	// focused before that bridge joined still counts as focused last.
	MethodPageState = "notifications/rooms/page_state"

	// MethodAgentsAllowed: page to bridge, {allowed}. The switch "let my
	// agents drive this tab", off by default. It pairs the page with every
	// session whose subject is the page's user.
	MethodAgentsAllowed = "notifications/rooms/agents_allowed"

	// MethodAcceptPairing: page to bridge, {code}. The page's user accepted
	// the code an agent got back from attach. The bridge that minted it
	// answers with MethodPairedByCode, naming the principal.
	MethodAcceptPairing = "notifications/rooms/accept_pairing"

	// MethodGrants: page to bridge, {grants: [Principal]}. Every principal
	// the page agrees to beyond its agents switch, as a full list each time.
	// The page sends it to every bridge that lists it, and again when it
	// changes, so the bridges in a room agree without sharing state.
	MethodGrants = "notifications/rooms/grants"

	// MethodPairingRequested: bridge to page, a PairingRequest. An agent
	// attached to the page's room and is not yet allowed to call it.
	MethodPairingRequested = "notifications/rooms/pairing_requested"

	// MethodToolRefused: bridge to page, {name, reason}. The bridge would not
	// offer one of the page's tools.
	MethodToolRefused = "notifications/rooms/tool_refused"

	// MethodPairingOffer: page to bridge, {code, ttlMs}. The code the page
	// shows its user, for an agent to enter with the pair tool. An empty code
	// withdraws the offer.
	MethodPairingOffer = "notifications/rooms/pairing_offer"

	// MethodPairedByCode: bridge to page, a PairedByCode. A principal paired
	// with the page by a code: one the page offered and an agent entered with
	// pair, or one an agent got from attach and the page accepted. The page
	// adds the grant and sends its grants again.
	MethodPairedByCode = "notifications/rooms/paired_by_code"
)

// MetaCaller is the _meta key on a tools/call the bridge sends a page, holding
// the caller's Principal from the bridge's validated token.
const MetaCaller = "io.mcpkit/rooms/caller"

// PairingRequest tells a page's user which agent asked to drive it. Subject
// and Client come from the caller's validated token. SameSubject is true when
// it is the page's own user, in which case the agents switch is enough;
// otherwise the user needs a pairing code.
type PairingRequest struct {
	Subject     string `json:"subject"`
	Client      string `json:"client,omitempty"`
	SameSubject bool   `json:"sameSubject"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

func (m rpcMessage) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

func encode(m rpcMessage) json.RawMessage {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	return b
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// randomID returns n random bytes as hex.
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

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
	// MethodPageState: page to bridge, {label, focused}. The label is what
	// list_pages shows; focused true stamps the page's last-focus time.
	MethodPageState = "notifications/rooms/page_state"

	// MethodAgentsAllowed: page to bridge, {allowed}. The switch "let my
	// agents drive this tab", off by default. It pairs the page with every
	// session whose subject is the page's user.
	MethodAgentsAllowed = "notifications/rooms/agents_allowed"

	// MethodAcceptPairing: page to bridge, {code}. The page's user accepted
	// the code an agent session got back from attach.
	MethodAcceptPairing = "notifications/rooms/accept_pairing"

	// MethodRevoke: page to bridge, {session}. Ends the page's pairing with
	// that session, or with every session when session is empty.
	MethodRevoke = "notifications/rooms/revoke"

	// MethodPairingRequested: bridge to page, a PairingRequest. A session
	// attached to the page's room and is not yet allowed to call it.
	MethodPairingRequested = "notifications/rooms/pairing_requested"

	// MethodToolRefused: bridge to page, {name, reason}. The bridge would not
	// offer one of the page's tools.
	MethodToolRefused = "notifications/rooms/tool_refused"
)

// PairingRequest tells a page's user which agent asked to drive it. Subject
// comes from the session's validated token. SameSubject is true when it is the
// page's own user, in which case the agents_allowed switch is enough; otherwise
// the user needs the code the agent was given.
type PairingRequest struct {
	Session     string `json:"session"`
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

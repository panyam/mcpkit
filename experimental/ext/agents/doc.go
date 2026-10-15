// Package agents implements server-declared agent discovery for MCP, tracking
// the agents-wg#22 Extensions Track SEP ("MCP Agent Discovery Extension").
//
// EXPERIMENTAL: the SEP has not merged, and the wire shape may still change.
// The package lives under experimental/ext/ (alongside events and protogen)
// and is promoted to ext/agents only once the SEP merges.
//
// # What it is
//
// A server groups its tools into agents and advertises them as a compact
// roster, instead of handing a supervisor host one flat tools/list. The host
// reads the roster, picks an agent, and only then pulls that agent's
// instructions and scoped tool schemas:
//
//	level 1  capabilities.extensions[io.modelcontextprotocol/agents]  "this server has agents"
//	level 2  agents/list                                                the roster, no tool schemas
//	level 3  agents/get {name}                                          one agent's instructions and tools
//
// Invocation is not new wire surface. The host builds a local subagent from
// the agents/get result and calls the scoped tools through the existing
// tools/call. An agent's tools are the server's own registered tools,
// resolved by name on every request, so agents/get always returns exactly
// what the same caller would see in tools/list.
package agents

// ExtensionID is the reverse-domain identifier a server advertises in its
// initialize (or server/discover) capabilities.extensions map to signal it
// speaks the agents discovery primitive. Clients gate agents/list and
// agents/get behind ServerSupportsExtension(ExtensionID).
//
// It is the identifier agents-wg#22 defines. It sat in mcpkit's namespace,
// io.mcpkit/agents, until this implementation matched the SEP, so a client
// recognising the standard identifier never negotiated a payload the SEP does
// not define. SEP-3392 keeps an identifier fixed through Experimental and
// Beta, so it should not change again. See NOTES.md.
const ExtensionID = "io.modelcontextprotocol/agents"

// SpecVersion is the draft the implementation tracks: agents-wg#22 at head
// 9b16840e, dated by that commit. Bump it when the implementation follows a
// newer revision of the SEP.
const SpecVersion = "2026-10-09"

// MethodList and MethodGet are the two JSON-RPC methods this primitive adds.
// Exported so both the server registry and the client SDK reference one
// literal each, and so a tolerant decoder can switch on them without
// re-stringing.
const (
	MethodList = "agents/list"
	MethodGet  = "agents/get"
)

// NotificationListChanged tells a client the roster may have changed. It is
// delivered only on a subscriptions/listen stream whose filter set
// ListChangedFilterKey, and on the legacy session wire's GET stream.
const NotificationListChanged = "notifications/agents/list_changed"

// ListChangedFilterKey is the subscriptions/listen filter key a client sets
// to true to receive NotificationListChanged. Register makes the server
// honor it.
const ListChangedFilterKey = "agentsListChanged"

// Package agents implements the experimental server-declared agent-definition
// discovery primitive for MCP (agents-wg#20, "Agent definition").
//
// EXPERIMENTAL: this is pre-SEP research surface. Method names, wire shape,
// and the extension ID may all change as the MCP Agents WG iterates. The
// package lives under experimental/ext/ (alongside events and protogen) and
// is free to churn until a SEP merges; promote to ext/agents only then.
//
// # What it is
//
// A server that hosts a fleet of specialist agents advertises them as a small
// roster of tuples for routing, instead of flattening every specialist's tools
// into one tools/list. A supervisor host sees N agent summaries (name,
// description, capability labels, example tasks), picks one, and only then
// pulls that specialist's instructions and scoped tool schemas. This is the
// same progressive-disclosure shape as two-tier skills loading (#910):
//
//	level 1  capabilities.extensions[io.modelcontextprotocol/agents]  — "this server has agents"
//	level 2  agents/list                                              — the roster, no tool schemas
//	level 3  agents/get {agentId}                                     — one agent's instructions + scoped tools
//
// Invocation is NOT new wire surface. Each agent advertises a delegateTool
// (e.g. "invoke_workflow_agent"); the host routes to the agent and calls that
// tool with the task via the existing tools/call. Only discovery is new here.
//
// # Deliberate non-coupling
//
// AgentSummary.TasksEnabled ties conceptually to SEP-2663 (an async delegate is
// a Task) and AgentSummary.SkillURI ties to the skills work, but this package
// couples to neither: TasksEnabled is an advertised bool and SkillURI is an
// advertised string. The bridge that turns an agents/get result into a
// Runner-backed AgentSource is agent-layer work (#1144), not here — per
// agent/CONSTRAINTS.md A6, this package traffics only in protocol objects.
package agents

// ExtensionID is the reverse-domain identifier a server advertises in its
// initialize (or server/discover) capabilities.extensions map to signal it
// speaks the agents discovery primitive. Clients gate agents/list and
// agents/get behind ServerSupportsExtension(ExtensionID).
//
// The value tracks the MCP Agents WG's working name. It is provisional: the
// WG has not chosen among the three wire shapes the research doc lists (RPC,
// resources under agent://, or an extension), and mcpkit ships the RPC shape
// behind this experimental extension advertisement.
const ExtensionID = "io.modelcontextprotocol/agents"

// SpecVersion is the draft the implementation tracks. There is no SEP yet, so
// this is a date-stamped placeholder aligned with the agents-wg#20 research
// doc rather than a spec version. Bump it when the WG advances the shape.
const SpecVersion = "2026-08-03"

// MethodList and MethodGet are the two JSON-RPC methods this primitive adds.
// Exported so both the server registry and the client SDK reference one
// literal each, and so a tolerant decoder can switch on them without
// re-stringing.
const (
	MethodList = "agents/list"
	MethodGet  = "agents/get"
)

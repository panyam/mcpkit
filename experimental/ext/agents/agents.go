package agents

import "github.com/panyam/mcpkit/core"

// AgentDef is a server author's declaration of one agent. It carries both the
// roster fields (returned by agents/list) and the detail fields (returned only
// by agents/get). The split is deliberate: the roster is cheap to send to
// every connecting host, while Instructions and the tool schemas are the
// payload a host pulls only after it has chosen this agent.
//
// A server hands a slice of these to Register. Name must be unique within a
// server; Register rejects duplicates.
type AgentDef struct {
	// Name is the agent's identifier within the server's roster
	// (e.g. "workflow-agent"). It is the agents/get lookup key.
	Name string

	// Description is a short summary of what the agent is for, shown to the
	// supervisor host's model so it can route without pulling tool schemas.
	Description string

	// Capabilities is a short list of routing labels ("approval status",
	// "pipeline failures"). They are descriptive metadata, not tool names.
	Capabilities []string

	// Instructions is optional guidance a host may use when invoking this
	// agent. Detail only: returned by agents/get, never by agents/list.
	Instructions string

	// ModelRoutingHint is optional, advisory guidance for host-side model
	// selection. Detail only. A server must not assume the host honored it.
	ModelRoutingHint *ModelRoutingHint

	// Tools names the server tools scoped to this agent. They are resolved
	// against the server's tool registry on every agents/get, so the returned
	// definitions are the ones tools/list serves and cannot drift from them.
	// Tool names are server-scoped: two agents listing the same name share one
	// callable tool.
	Tools []string
}

// Card projects an AgentDef onto its roster wire shape, dropping the detail
// fields. This is what agents/list returns per agent.
func (d AgentDef) Card() AgentCard {
	caps := d.Capabilities
	if caps == nil {
		caps = []string{}
	}
	return AgentCard{Name: d.Name, Description: d.Description, Capabilities: caps}
}

// AgentCard is the per-agent roster entry returned by agents/list. It carries
// routing fields only, never instructions or tool schemas.
type AgentCard struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// ModelRole is a relative model class a host maps onto the models available
// under its own policy. It is not a provider-specific identifier.
type ModelRole string

// The three model roles agents-wg#22 defines.
const (
	ModelRoleSmall  ModelRole = "small"
	ModelRoleMedium ModelRole = "medium"
	ModelRoleLarge  ModelRole = "large"
)

// ModelRoutingHint is the advisory, agent-scoped model guidance returned by
// agents/get. Vision is always serialized because the SEP makes it required.
type ModelRoutingHint struct {
	Role   ModelRole `json:"role,omitempty"`
	Vision bool      `json:"vision"`
}

// ListAgentsParams is the agents/list request params. Omit Cursor for the
// first page and pass the previous result's NextCursor for the next one.
type ListAgentsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListAgentsResult is the agents/list response. NextCursor is empty on the
// last page. TTLMs and CacheScope are the SEP-2549 cache hints for the roster
// as one cache entry.
type ListAgentsResult struct {
	ResultType core.ResultType `json:"resultType"`
	Agents     []AgentCard     `json:"agents"`
	NextCursor string          `json:"nextCursor,omitempty"`
	TTLMs      *int            `json:"ttlMs,omitempty"`
	CacheScope string          `json:"cacheScope,omitempty"`
}

// GetAgentParams is the agents/get request params. Name selects the agent.
type GetAgentParams struct {
	Name string `json:"name"`
}

// GetAgentResult is the agents/get response. Agent is the name of the agent
// the details belong to. Tools holds the full tool definitions, field for
// field what the same caller would see in tools/list. TTLMs and CacheScope
// are the SEP-2549 cache hints for this agent's entry, independent of the
// roster's.
type GetAgentResult struct {
	ResultType       core.ResultType   `json:"resultType"`
	Agent            string            `json:"agent"`
	Instructions     string            `json:"instructions,omitempty"`
	ModelRoutingHint *ModelRoutingHint `json:"modelRoutingHint,omitempty"`
	Tools            []core.ToolDef    `json:"tools"`
	TTLMs            *int              `json:"ttlMs,omitempty"`
	CacheScope       string            `json:"cacheScope,omitempty"`
}

// The data.reason values agents-wg#22 attaches to agents/get errors.
const (
	// ReasonAgentNotFound accompanies -32602 when the agent does not exist or
	// is hidden from the caller. The two cases are indistinguishable on the
	// wire by design.
	ReasonAgentNotFound = "agent_not_found"

	// ReasonInvalidAgentDefinition accompanies -32603 when an agent names a
	// tool the server catalog does not hold. It signals server
	// misconfiguration, never a caller authorization outcome.
	ReasonInvalidAgentDefinition = "invalid_agent_definition"
)

// ErrorData is the data object on an agents/get error.
type ErrorData struct {
	Reason string `json:"reason"`
}

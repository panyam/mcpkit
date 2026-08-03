package agents

import "github.com/panyam/mcpkit/core"

// AgentDef is a server author's full declaration of one delegatable agent.
// It carries both the roster fields (returned by agents/list) and the detail
// fields (returned only by agents/get). The split is deliberate: the roster is
// cheap to send to every connecting host, while Instructions and Tools are the
// expensive level-3 payload a host pulls only after it has chosen this agent.
//
// A server hands a slice of these to Register. AgentID must be unique within a
// server; Register rejects duplicates.
type AgentDef struct {
	// AgentID is the stable routing identifier (e.g. "workflow-agent"). Used
	// as the agents/get lookup key and echoed in both wire shapes.
	AgentID string

	// Description is a one-line summary of what the agent is for. Shown to the
	// supervisor host's model so it can route without pulling tool schemas.
	Description string

	// Capabilities is a short list of capability labels ("Approval gates",
	// "Run analysis"). Routing hints, not tool names.
	Capabilities []string

	// ExampleTasks are representative prompts the agent handles well. Included
	// in the roster because few-shot routing works better than description
	// alone.
	ExampleTasks []string

	// DelegateTool names the normal tool the host calls (via tools/call) to
	// invoke this agent with a task. Invocation is not new wire surface — this
	// is just the advertised entry point.
	DelegateTool string

	// TasksEnabled advertises that the delegate runs as a SEP-2663 async Task
	// rather than a synchronous call. Advisory only; this package does not
	// couple to ext/tasks.
	TasksEnabled bool

	// SkillURI optionally points at a SKILL.md-shaped resource describing the
	// agent in depth (skill://...). Advisory only; this package does not
	// couple to ext/skills.
	SkillURI string

	// Instructions is the agent's system prompt / behavioral contract. Detail
	// only — returned by agents/get, never by agents/list.
	Instructions string

	// Tools is the agent's scoped tool set: the schemas a host materializes
	// into a Runner once it has chosen this agent. Detail only — returned by
	// agents/get, never by agents/list. This is what keeps the supervisor from
	// eager-loading a flat tools/list of every specialist's schemas.
	Tools []core.ToolDef
}

// Summary projects an AgentDef onto its roster (level-2) wire shape, dropping
// the Instructions and Tools detail. This is what agents/list returns per
// agent.
func (d AgentDef) Summary() AgentSummary {
	return AgentSummary{
		AgentID:      d.AgentID,
		Description:  d.Description,
		Capabilities: d.Capabilities,
		ExampleTasks: d.ExampleTasks,
		DelegateTool: d.DelegateTool,
		TasksEnabled: d.TasksEnabled,
		SkillURI:     d.SkillURI,
	}
}

// Detail projects an AgentDef onto its full (level-3) wire shape: the Summary
// fields plus Instructions and the scoped Tools. This is what agents/get
// returns.
func (d AgentDef) Detail() AgentDetail {
	return AgentDetail{
		AgentSummary: d.Summary(),
		Instructions: d.Instructions,
		Tools:        d.Tools,
	}
}

// AgentSummary is the per-agent wire shape returned by agents/list. It carries
// routing fields only — no Instructions, no Tools. This is the roster a
// supervisor host reads before deciding which specialist to pull.
//
// The JSON tags match the agents-wg#20 research doc §6 payload verbatim so the
// shape doubles as an interop anchor against the WG proposal.
type AgentSummary struct {
	AgentID      string   `json:"agentId"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	ExampleTasks []string `json:"exampleTasks,omitempty"`
	DelegateTool string   `json:"delegateTool,omitempty"`
	TasksEnabled bool     `json:"tasksEnabled,omitempty"`
	SkillURI     string   `json:"skillUri,omitempty"`
}

// AgentDetail is the wire shape returned by agents/get: an AgentSummary plus
// the level-3 payload (instructions + scoped tool schemas). AgentSummary is
// embedded so its JSON fields flatten into the same object the roster uses,
// and a get response is a strict superset of a list entry.
type AgentDetail struct {
	AgentSummary
	Instructions string         `json:"instructions,omitempty"`
	Tools        []core.ToolDef `json:"tools,omitempty"`
}

// ListResult is the agents/list response envelope.
type ListResult struct {
	Agents []AgentSummary `json:"agents"`
}

// GetParams is the agents/get request params. AgentID selects the agent.
type GetParams struct {
	AgentID string `json:"agentId"`
}

// GetResult is the agents/get response envelope. Agent is the resolved detail.
type GetResult struct {
	Agent AgentDetail `json:"agent"`
}

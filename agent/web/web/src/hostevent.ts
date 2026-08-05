// hostevent.ts mirrors agent/host/hostevent.go on the wire. A Watch Frame's
// payload is json.Marshal(HostEvent) — the Go struct carries no json tags, so
// its keys are the Go field names verbatim (Kind, RunnerEvent, ...). The nested
// agent.Event and core.ElicitationRequest DO carry json tags, so those are
// lowercase (kind, text, message). Keep this file in step with the Go source;
// it is the only place the wire shape is spelled out on the client.

// HostEventKind is the Frame.kind discriminator (HostEvent.Kind). The first
// block is what the #1197 shell renders; the second is the observability set
// #1198's panels project (sub-agent tree, timeline, memory, tools, budgets).
export const HostEventKind = {
  RunnerEvent: "runner-event",
  CommandResult: "command-result",
  TurnDone: "turn-done",
  TurnFailed: "turn-failed",
  SessionChanged: "session-changed",
  Message: "message",
  ElicitRequest: "elicit-request",
  ElicitResolved: "elicit-resolved",
  // observability kinds (#1198)
  SubAgentEvent: "sub-agent-event",
  SessionWarn: "session-warn",
  TriggerFired: "trigger-fired",
  SkillsLoaded: "skills-loaded",
  SkillSkipped: "skill-skipped",
  EventDropped: "event-dropped",
  TaskStatus: "task-status",
  TaskDetached: "task-detached",
  TaskCompleted: "task-completed",
  ServerStateChanged: "server-state-changed",
  Handoff: "handoff",
} as const;

// EventKind is agent.Event.kind, the streaming-turn discriminator.
export const EventKind = {
  TurnBegin: "turn-begin",
  TextDelta: "text-delta",
  ThinkingBegin: "thinking-begin",
  ThinkingDelta: "thinking-delta",
  ThinkingEnd: "thinking-end",
  ToolBegin: "tool-begin",
  ToolEnd: "tool-end",
  ToolError: "tool-error",
  ToolDenied: "tool-denied",
  ToolCancelled: "tool-cancelled",
  ToolUnavailable: "tool-unavailable",
  Compaction: "compaction",
  Signal: "signal",
  TurnEnd: "turn-end",
  Error: "error",
} as const;

// AgentEvent is one streaming-turn event (agent.Event). Only the fields the
// panels read are typed; the rest ride along untyped. The nested struct carries
// json tags so these keys are lowercase (kind, toolCall, toolResult).
export interface AgentEvent {
  kind: string;
  step?: number;
  text?: string;
  toolCall?: { id: string; name: string; args?: unknown };
  toolResult?: ToolResult;
  result?: { text?: string; usage?: Usage; steps?: number; finishReason?: string };
  compaction?: { before: number; after: number };
  reason?: string;
  error?: string;
}

// ToolResult mirrors the fields core.ToolResult exposes over the wire that the
// tool inspector reads: whether it errored and its content items (text is the
// projected first text item).
export interface ToolResult {
  isError?: boolean;
  content?: { type: string; text?: string }[];
}

// Usage is agent.Usage (token counts on a turn result).
export interface Usage {
  inputTokens?: number;
  outputTokens?: number;
}

// SubAgentEnvelope is agent.SubAgentEvent (no json tags, so the keys are the Go
// field names verbatim): the child's Event plus the Scope/Depth on the envelope.
export interface SubAgentEnvelope {
  Scope: string;
  Depth: number;
  Event: AgentEvent;
}

// ElicitationRequest is core.ElicitationRequest — the pending-ask prompt.
export interface ElicitationRequest {
  message: string;
  mode?: string;
  url?: string;
}

// HostEvent is the decoded Frame payload (agent/host.HostEvent). It is a tagged
// union keyed by Kind; only the fields named for the Kind are set.
export interface HostEvent {
  Kind: string;
  RunnerEvent?: AgentEvent;
  Result?: { text?: string; usage?: Usage; steps?: number; finishReason?: string };
  Err?: string;
  RunID?: string;
  Label?: string;
  Message?: string;
  From?: string;
  To?: string;
  AskID?: number;
  Elicit?: ElicitationRequest;
  By?: string;
  SubAgent?: SubAgentEnvelope;
  ServerID?: string;
  ServerState?: string;
  URI?: string;
  EventName?: string;
  Loaded?: number;
  Skipped?: number;
}

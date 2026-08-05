// hostevent.ts mirrors agent/host/hostevent.go on the wire. A Watch Frame's
// payload is json.Marshal(HostEvent) — the Go struct carries no json tags, so
// its keys are the Go field names verbatim (Kind, RunnerEvent, ...). The nested
// agent.Event and core.ElicitationRequest DO carry json tags, so those are
// lowercase (kind, text, message). Keep this file in step with the Go source;
// it is the only place the wire shape is spelled out on the client.

// HostEventKind is the Frame.kind discriminator (HostEvent.Kind). Only the
// kinds the shell renders today are named; #1198 adds the observability kinds.
export const HostEventKind = {
  RunnerEvent: "runner-event",
  CommandResult: "command-result",
  TurnDone: "turn-done",
  TurnFailed: "turn-failed",
  SessionChanged: "session-changed",
  Message: "message",
  ElicitRequest: "elicit-request",
  ElicitResolved: "elicit-resolved",
} as const;

// EventKind is agent.Event.kind, the streaming-turn discriminator.
export const EventKind = {
  TurnBegin: "turn-begin",
  TextDelta: "text-delta",
  ThinkingDelta: "thinking-delta",
  ToolBegin: "tool-begin",
  ToolEnd: "tool-end",
  TurnEnd: "turn-end",
  Error: "error",
} as const;

// AgentEvent is one streaming-turn event (agent.Event). Only the fields the
// shell reads are typed; the rest ride along untyped.
export interface AgentEvent {
  kind: string;
  step?: number;
  text?: string;
  toolCall?: { id: string; name: string; args?: unknown };
  error?: string;
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
  Result?: unknown;
  Err?: string;
  RunID?: string;
  Message?: string;
  From?: string;
  To?: string;
  AskID?: number;
  Elicit?: ElicitationRequest;
  By?: string;
}

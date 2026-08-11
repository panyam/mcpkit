import { createSignal, type Accessor } from "solid-js";
import { EventKind, HostEventKind, type AgentEvent, type HostEvent } from "./hostevent.js";

// subagents.ts projects the HostSubAgentEvent stream into a tree of nested
// sub-agent activity. Each sub-agent event carries a slash-joined Scope
// ("researcher" or "researcher/summarizer") and a Depth on the envelope; the
// inner agent.Event is the child's own turn-lifecycle event. This reduce mirrors
// conversation.ts: a framework-light store with an ingest(ev) sink and Solid
// accessors, so it unit-tests without a DOM (the assembly is the tricky part).

// SubAgentStatus is the derived lifecycle state of one node, from its latest
// inner event. idle is the seed before any turn event lands.
export type SubAgentStatus = "idle" | "running" | "done" | "error";

// SubAgentNode is one agent in the tree. scope is the full slash path; name is
// its last segment. depth mirrors the envelope (1 = top-level). activity is a
// short human line for the latest thing it did; toolCalls counts tool-begins;
// resultText is its final answer once it ends.
export interface SubAgentNode {
  scope: string;
  name: string;
  depth: number;
  status: SubAgentStatus;
  activity: string;
  toolCalls: number;
  resultText: string;
}

// SubAgentStore holds the projected tree. tree() is the pre-order flattening
// (each root followed by its descendants, both in first-seen order) that a panel
// renders as an indented list, using node.depth for the indent.
export interface SubAgentStore {
  tree: Accessor<SubAgentNode[]>;
  count: Accessor<number>;
  ingest: (ev: HostEvent) => void;
  reset: () => void;
}

// parentScope returns the scope of a node's parent, or "" for a root.
function parentScope(scope: string): string {
  const i = scope.lastIndexOf("/");
  return i < 0 ? "" : scope.slice(0, i);
}

// lastSegment is the node's own name within its parent.
function lastSegment(scope: string): string {
  const i = scope.lastIndexOf("/");
  return i < 0 ? scope : scope.slice(i + 1);
}

// activityFor maps an inner agent.Event to a node's status + one-line activity.
// It returns undefined for kinds a node does not react to (thinking deltas), so
// the caller leaves the prior state untouched.
function activityFor(e: AgentEvent): { status: SubAgentStatus; activity: string } | undefined {
  switch (e.kind) {
    case EventKind.TurnBegin:
      return { status: "running", activity: "started" };
    case EventKind.TextDelta:
      return { status: "running", activity: "responding…" };
    case EventKind.ToolBegin:
      return { status: "running", activity: `calling ${e.toolCall?.name ?? "tool"}…` };
    case EventKind.ToolEnd:
      return { status: "running", activity: `${e.toolCall?.name ?? "tool"} returned` };
    case EventKind.TurnEnd:
      return { status: "done", activity: "done" };
    case EventKind.Error:
      return { status: "error", activity: e.error ?? "error" };
    default:
      return undefined;
  }
}

export function createSubAgentStore(): SubAgentStore {
  const [tree, setTree] = createSignal<SubAgentNode[]>([]);

  // nodes is the identity map; order preserves first-seen insertion so the
  // flattening is stable. children lets the pre-order walk nest each agent under
  // the parent its scope names, regardless of which event arrived first.
  const nodes = new Map<string, SubAgentNode>();
  const order: string[] = [];

  // ensure returns the node for scope, creating it (and any missing ancestors)
  // on first sight so an inner event that arrives before its parent's still
  // slots the child under a placeholder parent rather than dropping it.
  const ensure = (scope: string, depth: number): SubAgentNode => {
    const existing = nodes.get(scope);
    if (existing) return existing;
    const parent = parentScope(scope);
    if (parent) ensure(parent, Math.max(1, depth - 1));
    const node: SubAgentNode = {
      scope,
      name: lastSegment(scope),
      depth: depth > 0 ? depth : 1,
      status: "idle",
      activity: "",
      toolCalls: 0,
      resultText: "",
    };
    nodes.set(scope, node);
    order.push(scope);
    return node;
  };

  // flatten walks roots (depth-1 / no slash) in first-seen order, emitting each
  // node before its children — the pre-order a tree renderer wants.
  const flatten = (): SubAgentNode[] => {
    const childrenOf = new Map<string, string[]>();
    const roots: string[] = [];
    for (const scope of order) {
      const parent = parentScope(scope);
      if (!parent) roots.push(scope);
      else (childrenOf.get(parent) ?? childrenOf.set(parent, []).get(parent)!).push(scope);
    }
    const out: SubAgentNode[] = [];
    const walk = (scope: string) => {
      const n = nodes.get(scope);
      if (n) out.push({ ...n });
      for (const child of childrenOf.get(scope) ?? []) walk(child);
    };
    for (const r of roots) walk(r);
    return out;
  };

  const ingest = (ev: HostEvent): void => {
    if (ev.Kind !== HostEventKind.SubAgentEvent || !ev.SubAgent) return;
    const { Scope: scope, Depth: depth, Event: inner } = ev.SubAgent;
    if (!scope || !inner) return;
    const node = ensure(scope, depth);
    if (inner.kind === EventKind.ToolBegin) node.toolCalls += 1;
    if (inner.kind === EventKind.TurnEnd && inner.result?.text) node.resultText = inner.result.text;
    const a = activityFor(inner);
    if (a) {
      node.status = a.status;
      node.activity = a.activity;
    }
    setTree(flatten());
  };

  const reset = () => {
    nodes.clear();
    order.length = 0;
    setTree([]);
  };

  return { tree, count: () => tree().length, ingest, reset };
}

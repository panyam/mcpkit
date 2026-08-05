import { createSignal, type Accessor } from "solid-js";
import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

// timeline.ts projects the whole HostEvent stream into a scrollable, filterable
// ledger: one entry per event, tagged by kind with a short human summary. It is
// the raw audit view beside the conversation — every domain event the host
// announced, in order. Bounded to the most recent MAX_ENTRIES so a long session
// does not grow without bound (Watch replays from offset 0, so the reduce is
// idempotent over a reconnect and simply refills the ring).

// MAX_ENTRIES caps the retained ledger. The newest are kept.
export const MAX_ENTRIES = 500;

// TimelineEntry is one row: a monotonic seq for keying, the HostEvent kind, and
// a one-line summary. For runner events the effective kind is the inner
// agent.Event kind so the filter distinguishes text from tool activity.
export interface TimelineEntry {
  seq: number;
  kind: string;
  summary: string;
}

// TimelineStore holds the ledger plus the set of kinds seen (for a filter UI)
// and the active filter. entries() applies the filter; allEntries() is the raw
// ring for counting.
export interface TimelineStore {
  entries: Accessor<TimelineEntry[]>;
  kinds: Accessor<string[]>;
  filter: Accessor<string>;
  setFilter: (kind: string) => void;
  ingest: (ev: HostEvent) => void;
  reset: () => void;
}

// summarize renders a compact one-line description of a HostEvent. The runner
// case unwraps the inner agent.Event so tool and text activity read distinctly.
export function summarize(ev: HostEvent): { kind: string; summary: string } {
  switch (ev.Kind) {
    case HostEventKind.RunnerEvent: {
      const re = ev.RunnerEvent;
      const inner = re?.kind ?? "runner-event";
      switch (inner) {
        case EventKind.TextDelta:
          return { kind: inner, summary: (re?.text ?? "").trim() || "(text)" };
        case EventKind.ToolBegin:
          return { kind: inner, summary: `→ ${re?.toolCall?.name ?? "tool"}` };
        case EventKind.ToolEnd:
          return { kind: inner, summary: `${re?.toolCall?.name ?? "tool"} ${re?.toolResult?.isError ? "✗" : "✓"}` };
        case "tool-error":
        case "tool-denied":
        case "tool-cancelled":
        case "tool-unavailable":
          return { kind: inner, summary: `${re?.toolCall?.name ?? "tool"}: ${re?.reason || re?.error || ""}`.trim() };
        case "compaction":
          return { kind: inner, summary: `compacted ${re?.compaction?.before ?? "?"} → ${re?.compaction?.after ?? "?"} msgs` };
        default:
          return { kind: inner, summary: inner };
      }
    }
    case HostEventKind.SubAgentEvent:
      return { kind: ev.Kind, summary: `[${ev.SubAgent?.Scope ?? ""}] ${ev.SubAgent?.Event?.kind ?? ""}`.trim() };
    case HostEventKind.TurnDone:
      return { kind: ev.Kind, summary: `turn done (${ev.Result?.steps ?? 0} steps)` };
    case HostEventKind.TurnFailed:
      return { kind: ev.Kind, summary: ev.Err || "turn failed" };
    case HostEventKind.SessionChanged:
      return { kind: ev.Kind, summary: `session ${ev.RunID || "(none)"}` };
    case HostEventKind.SessionWarn:
      return { kind: ev.Kind, summary: ev.Err || "persistence degraded" };
    case HostEventKind.TriggerFired:
      return { kind: ev.Kind, summary: ev.Label || "trigger" };
    case HostEventKind.SkillsLoaded:
      return { kind: ev.Kind, summary: `${ev.ServerID ?? ""}: ${ev.Loaded ?? 0} loaded, ${ev.Skipped ?? 0} skipped` };
    case HostEventKind.SkillSkipped:
      return { kind: ev.Kind, summary: `${ev.URI ?? ""}: ${ev.Err ?? ""}`.trim() };
    case HostEventKind.EventDropped:
      return { kind: ev.Kind, summary: `${ev.ServerID ?? ""} dropped ${ev.EventName ?? ""}`.trim() };
    case HostEventKind.ServerStateChanged:
      return { kind: ev.Kind, summary: `${ev.ServerID ?? ""} → ${ev.ServerState ?? ""}`.trim() };
    case HostEventKind.Handoff:
      return { kind: ev.Kind, summary: `${ev.From ?? "?"} → ${ev.To ?? "?"}` };
    case HostEventKind.Message:
      return { kind: ev.Kind, summary: ev.Message || "" };
    case HostEventKind.ElicitRequest:
      return { kind: ev.Kind, summary: ev.Elicit?.message || "ask" };
    case HostEventKind.ElicitResolved:
      return { kind: ev.Kind, summary: `resolved by ${ev.By || "?"}` };
    default:
      return { kind: ev.Kind || "(unknown)", summary: "" };
  }
}

export function createTimelineStore(): TimelineStore {
  const [entries, setEntries] = createSignal<TimelineEntry[]>([]);
  const [kinds, setKinds] = createSignal<string[]>([]);
  const [filter, setFilter] = createSignal("");
  let seq = 0;
  const seen = new Set<string>();
  let ring: TimelineEntry[] = [];

  const apply = () => {
    const f = filter();
    setEntries(f ? ring.filter((e) => e.kind === f) : [...ring]);
  };

  const ingest = (ev: HostEvent): void => {
    if (!ev.Kind) return;
    const { kind, summary } = summarize(ev);
    ring.push({ seq: seq++, kind, summary });
    if (ring.length > MAX_ENTRIES) ring = ring.slice(ring.length - MAX_ENTRIES);
    if (!seen.has(kind)) {
      seen.add(kind);
      setKinds([...seen].sort());
    }
    apply();
  };

  return {
    entries,
    kinds,
    filter,
    setFilter: (k) => {
      setFilter(k);
      apply();
    },
    ingest,
    reset: () => {
      ring = [];
      seq = 0;
      seen.clear();
      setKinds([]);
      setFilter("");
      setEntries([]);
    },
  };
}

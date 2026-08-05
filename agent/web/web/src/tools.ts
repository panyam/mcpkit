import { createSignal, type Accessor } from "solid-js";
import { EventKind, HostEventKind, type HostEvent, type ToolResult } from "./hostevent.js";

// tools.ts projects the tool-call lifecycle off the runner stream into an
// inspector: one row per call, matched begin→end by the provider call id, with
// its arguments, terminal status, and a result preview. It also detects an
// offloaded result — when OffloadingSource replaces a large result with a stub
// ("[tool result NB, stored as res:...]") — and surfaces the ref, since the
// blob itself is fetched internally via read_tool_result and is not exposed on
// the web bridge (so the panel shows the stub + ref, not the full blob).

// ToolStatus is the terminal disposition of a call (running until it lands).
export type ToolStatus = "running" | "ok" | "error" | "denied" | "cancelled" | "unavailable";

// ToolCallEntry is one inspected call.
export interface ToolCallEntry {
  id: string;
  name: string;
  args: string;
  status: ToolStatus;
  preview: string;
  offloaded: boolean;
  ref: string;
}

export interface ToolStore {
  calls: Accessor<ToolCallEntry[]>;
  offloadedCount: Accessor<number>;
  ingest: (ev: HostEvent) => void;
  reset: () => void;
}

// OFFLOAD_STUB matches the stub OffloadingSource writes, capturing the ref. The
// stub text is "[tool result <N>B, stored as <ref>]\npreview: ...".
const OFFLOAD_STUB = /stored as (\S+?)\]/;

// detectOffload returns the offload ref if text is an offload stub, else "".
export function detectOffload(text: string): string {
  const m = OFFLOAD_STUB.exec(text);
  return m ? m[1] : "";
}

// resultText returns the first text content item of a tool result, or "".
function resultText(r: ToolResult | undefined): string {
  if (!r?.content) return "";
  for (const c of r.content) if (c.type === "text" && c.text) return c.text;
  return "";
}

function snippet(s: string, n = 160): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n) + "…" : t;
}

export function createToolStore(): ToolStore {
  const [calls, setCalls] = createSignal<ToolCallEntry[]>([]);
  let ring: ToolCallEntry[] = [];
  const byId = new Map<string, ToolCallEntry>();

  const emit = () => setCalls([...ring]);

  // upsert finds the entry for a call id, creating one if a terminal event
  // arrives without a preceding tool-begin (defensive over a replay boundary).
  const upsert = (id: string, name: string): ToolCallEntry => {
    let e = byId.get(id);
    if (!e) {
      e = { id, name, args: "", status: "running", preview: "", offloaded: false, ref: "" };
      byId.set(id, e);
      ring = [...ring, e];
    }
    if (name && !e.name) e.name = name;
    return e;
  };

  const ingest = (ev: HostEvent): void => {
    if (ev.Kind !== HostEventKind.RunnerEvent) return;
    const re = ev.RunnerEvent;
    if (!re) return;
    const tc = re.toolCall;
    const id = tc?.id ?? "";
    switch (re.kind) {
      case EventKind.ToolBegin: {
        if (!id) return;
        const e = upsert(id, tc?.name ?? "tool");
        e.args = tc?.args === undefined ? "" : snippet(JSON.stringify(tc.args), 200);
        emit();
        break;
      }
      case EventKind.ToolEnd: {
        if (!id) return;
        const e = upsert(id, tc?.name ?? "tool");
        const text = resultText(re.toolResult);
        e.status = re.toolResult?.isError ? "error" : "ok";
        e.preview = snippet(text);
        const ref = detectOffload(text);
        if (ref) {
          e.offloaded = true;
          e.ref = ref;
        }
        emit();
        break;
      }
      case EventKind.ToolError:
        if (id) {
          upsert(id, tc?.name ?? "tool").status = "error";
          byId.get(id)!.preview = snippet(re.error ?? "");
          emit();
        }
        break;
      case EventKind.ToolDenied:
      case EventKind.ToolCancelled:
      case EventKind.ToolUnavailable: {
        if (!id) return;
        const e = upsert(id, tc?.name ?? "tool");
        e.status = re.kind === EventKind.ToolDenied ? "denied" : re.kind === EventKind.ToolCancelled ? "cancelled" : "unavailable";
        e.preview = snippet(re.reason ?? "");
        emit();
        break;
      }
    }
  };

  return {
    calls,
    offloadedCount: () => ring.filter((c) => c.offloaded).length,
    ingest,
    reset: () => {
      ring = [];
      byId.clear();
      setCalls([]);
    },
  };
}

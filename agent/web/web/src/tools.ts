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
  const idx = new Map<string, number>();

  // update writes a NEW entry object for id (creating one if a terminal event
  // arrives without a preceding tool-begin, defensive over a replay boundary),
  // then re-emits. New objects, not in-place mutation: Solid's <For> keys rows
  // by reference, so a mutated-in-place entry would never re-render.
  const update = (id: string, name: string, patch: Partial<ToolCallEntry>): void => {
    const base: ToolCallEntry =
      idx.has(id) ? ring[idx.get(id)!] : { id, name: name || "tool", args: "", status: "running", preview: "", offloaded: false, ref: "" };
    const next = { ...base, ...patch };
    if (name && !next.name) next.name = name;
    if (idx.has(id)) ring = ring.map((e, i) => (i === idx.get(id)! ? next : e));
    else {
      idx.set(id, ring.length);
      ring = [...ring, next];
    }
    setCalls(ring);
  };

  const ingest = (ev: HostEvent): void => {
    if (ev.Kind !== HostEventKind.RunnerEvent) return;
    const re = ev.RunnerEvent;
    if (!re) return;
    const tc = re.toolCall;
    const id = tc?.id ?? "";
    const name = tc?.name ?? "tool";
    switch (re.kind) {
      case EventKind.ToolBegin:
        if (id) update(id, name, { args: tc?.args === undefined ? "" : snippet(JSON.stringify(tc.args), 200) });
        break;
      case EventKind.ToolEnd: {
        if (!id) return;
        const text = resultText(re.toolResult);
        const ref = detectOffload(text);
        update(id, name, {
          status: re.toolResult?.isError ? "error" : "ok",
          preview: snippet(text),
          offloaded: ref !== "",
          ref,
        });
        break;
      }
      case EventKind.ToolError:
        if (id) update(id, name, { status: "error", preview: snippet(re.error ?? "") });
        break;
      case EventKind.ToolDenied:
        if (id) update(id, name, { status: "denied", preview: snippet(re.reason ?? "") });
        break;
      case EventKind.ToolCancelled:
        if (id) update(id, name, { status: "cancelled", preview: snippet(re.reason ?? "") });
        break;
      case EventKind.ToolUnavailable:
        if (id) update(id, name, { status: "unavailable", preview: snippet(re.reason ?? "") });
        break;
    }
  };

  return {
    calls,
    offloadedCount: () => ring.filter((c) => c.offloaded).length,
    ingest,
    reset: () => {
      ring = [];
      idx.clear();
      setCalls([]);
    },
  };
}

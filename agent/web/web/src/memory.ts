import { createSignal, type Accessor } from "solid-js";
import type { Client } from "@connectrpc/connect";
import type { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";
import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

// memory.ts is the memory inspector projection. Two sources feed it: the
// compaction events off the runner stream (the only memory-lifecycle event the
// agent.Event vocabulary carries — recall and injection are transient pre-turn
// transforms, not events), and an on-demand read of the working-memory summary
// via Dispatch("/memory") → CmdResult.Message. The event side is a pure reduce
// (unit-testable); the summary is a manual refresh a panel button drives.

// Compaction is one recorded compaction pass: the message counts before/after
// the head was summarized.
export interface Compaction {
  seq: number;
  before: number;
  after: number;
}

export interface MemoryStore {
  compactions: Accessor<Compaction[]>;
  summary: Accessor<string>;
  refreshing: Accessor<boolean>;
  ingest: (ev: HostEvent) => void;
  refresh: () => Promise<void>;
  reset: () => void;
}

// decodeCmdMessage pulls the Message field out of a Dispatch payload
// (json.Marshal(CmdResult)); returns "" if it does not decode.
export function decodeCmdMessage(payload: Uint8Array): string {
  try {
    const cmd = JSON.parse(new TextDecoder().decode(payload)) as { Message?: string };
    return cmd.Message ?? "";
  } catch {
    return "";
  }
}

export function createMemoryStore(client: Client<typeof HostService>): MemoryStore {
  const [compactions, setCompactions] = createSignal<Compaction[]>([]);
  const [summary, setSummary] = createSignal("");
  const [refreshing, setRefreshing] = createSignal(false);
  let seq = 0;
  let ring: Compaction[] = [];

  const ingest = (ev: HostEvent): void => {
    if (ev.Kind !== HostEventKind.RunnerEvent) return;
    const re = ev.RunnerEvent;
    if (re?.kind === EventKind.Compaction && re.compaction) {
      ring = [...ring, { seq: seq++, before: re.compaction.before, after: re.compaction.after }];
      setCompactions(ring);
    }
  };

  const refresh = async (): Promise<void> => {
    setRefreshing(true);
    try {
      const resp = await client.dispatch({ line: "/memory" });
      setSummary(decodeCmdMessage(resp.payload) || "(no summary)");
    } catch (e) {
      setSummary(e instanceof Error ? e.message : String(e));
    } finally {
      setRefreshing(false);
    }
  };

  return {
    compactions,
    summary,
    refreshing,
    ingest,
    refresh,
    reset: () => {
      ring = [];
      seq = 0;
      setCompactions([]);
      setSummary("");
    },
  };
}

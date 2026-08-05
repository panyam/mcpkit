import { createSignal, type Accessor } from "solid-js";
import type { Client } from "@connectrpc/connect";
import type { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";
import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

// Turn is one committed line of the conversation transcript.
export interface Turn {
  role: "user" | "assistant" | "system";
  text: string;
}

// PendingAsk is a live elicitation/approval the host broadcast to every surface.
// id is the event-log offset (HostEvent.AskID) a surface passes back to
// RespondToAsk; the first responder across all surfaces wins.
export interface PendingAsk {
  id: number;
  message: string;
}

// Status is the status-line read (active model + session id).
export interface Status {
  model: string;
  session: string;
}

// ConversationStore is the shared, framework-light state behind every surface
// that shows the conversation: the DockView Conversation panel and the mobile
// overlay both read the same accessors, so a turn streamed once is reflected in
// both. ingest(ev) folds one HostEvent into the state; it is the WatchStream's
// sink. submit / respondToAsk are the two writes back to the host.
export interface ConversationStore {
  turns: Accessor<Turn[]>;
  streaming: Accessor<string>;
  activity: Accessor<string>;
  status: Accessor<Status>;
  ask: Accessor<PendingAsk | null>;
  error: Accessor<string>;
  busy: Accessor<boolean>;
  ingest: (ev: HostEvent) => void;
  submit: (input: string) => Promise<void>;
  respondToAsk: (action: "accept" | "decline" | "cancel") => Promise<void>;
  setStatus: (s: Status) => void;
}

export function createConversationStore(client: Client<typeof HostService>): ConversationStore {
  const [turns, setTurns] = createSignal<Turn[]>([]);
  const [streaming, setStreaming] = createSignal("");
  const [activity, setActivity] = createSignal("");
  const [status, setStatus] = createSignal<Status>({ model: "", session: "" });
  const [ask, setAsk] = createSignal<PendingAsk | null>(null);
  const [error, setError] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const push = (t: Turn) => setTurns((prev) => [...prev, t]);

  // commitAssistant flushes the in-progress assistant text into a committed turn.
  const commitAssistant = () => {
    const text = streaming().trim();
    setStreaming("");
    setActivity("");
    if (text) push({ role: "assistant", text });
  };

  const ingest = (ev: HostEvent): void => {
    switch (ev.Kind) {
      case HostEventKind.RunnerEvent: {
        const re = ev.RunnerEvent;
        if (!re) return;
        switch (re.kind) {
          case EventKind.TextDelta:
            setStreaming((s) => s + (re.text ?? ""));
            break;
          case EventKind.ToolBegin:
            setActivity(`calling ${re.toolCall?.name ?? "tool"}…`);
            break;
          case EventKind.ToolEnd:
            setActivity("");
            break;
          case EventKind.TurnEnd:
            commitAssistant();
            break;
        }
        break;
      }
      case HostEventKind.TurnDone:
        commitAssistant();
        break;
      case HostEventKind.TurnFailed:
        commitAssistant();
        setError(ev.Err ?? "turn failed");
        break;
      case HostEventKind.Message:
        if (ev.Message) push({ role: "system", text: ev.Message });
        break;
      case HostEventKind.SessionChanged:
        setStatus((s) => ({ ...s, session: ev.RunID ?? "" }));
        break;
      case HostEventKind.ElicitRequest:
        setAsk({ id: ev.AskID ?? 0, message: ev.Elicit?.message ?? "(approval requested)" });
        break;
      case HostEventKind.ElicitResolved:
        // Every surface retracts its prompt once any surface answered.
        setAsk(null);
        break;
    }
  };

  const submit = async (input: string): Promise<void> => {
    const text = input.trim();
    if (!text || busy()) return;
    setError("");
    push({ role: "user", text });
    setBusy(true);
    try {
      await client.submit({ input: text });
    } catch (e) {
      // A failed turn is also announced as a turn-failed frame; surface the
      // transport error too so a submit that never reached the host is visible.
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const respondToAsk = async (action: "accept" | "decline" | "cancel"): Promise<void> => {
    const pending = ask();
    if (!pending) return;
    setAsk(null); // optimistic retract; the elicit-resolved frame confirms it
    const result = new TextEncoder().encode(JSON.stringify({ action }));
    try {
      await client.respondToAsk({ askId: BigInt(pending.id), result, by: "web" });
    } catch {
      // Another surface won the ask first (ErrAlreadyResolved) — the prompt is
      // already retracted, so nothing to do.
    }
  };

  return {
    turns,
    streaming,
    activity,
    status,
    ask,
    error,
    busy,
    ingest,
    submit,
    respondToAsk,
    setStatus: (s) => setStatus(s),
  };
}

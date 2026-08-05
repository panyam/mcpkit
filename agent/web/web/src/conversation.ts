import { createSignal, type Accessor } from "solid-js";
import type { Client } from "@connectrpc/connect";
import type { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";
import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

// Turn is one committed line of the conversation transcript. from names the
// surface a turn originated on when it was NOT this browser (e.g. a turn started
// on the terminal streams here over Watch with no local user bubble); it is
// undefined for a turn this surface submitted.
export interface Turn {
  role: "user" | "assistant" | "system";
  text: string;
  from?: string;
}

// PendingAsk is a live elicitation/approval the host broadcast to every surface.
// id is the event-log offset (HostEvent.AskID) a surface passes back to
// RespondToAsk; the first responder across all surfaces wins.
export interface PendingAsk {
  id: number;
  message: string;
}

// ResolvedNotice is the retraction receipt shown after an ask this surface was
// displaying was answered somewhere else. text reads "answered on terminal" /
// "answered in another browser tab"; by is the raw resolving-surface tag. It is
// null when this surface answered its own ask (the prompt just retracts).
export interface ResolvedNotice {
  by: string;
  text: string;
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
  resolved: Accessor<ResolvedNotice | null>;
  fromOther: Accessor<boolean>;
  error: Accessor<string>;
  busy: Accessor<boolean>;
  ingest: (ev: HostEvent) => void;
  submit: (input: string) => Promise<void>;
  respondToAsk: (action: "accept" | "decline" | "cancel") => Promise<void>;
  dismissResolved: () => void;
  setStatus: (s: Status) => void;
}

// surfaceLabel maps a resolving-surface tag (HostEvent.By) to a display noun.
// The terminal responder resolves as "local" (App.barrierElicit); a web RPC
// defaults to "web". Unknown tags render verbatim.
function surfaceLabel(by: string): string {
  switch (by) {
    case "local":
    case "tui":
    case "terminal":
      return "terminal";
    case "web":
    case "browser":
      return "browser";
    case "":
      return "another surface";
    default:
      return by;
  }
}

// resolutionText renders the "answered elsewhere" receipt. This is only reached
// when another surface answered (a self-answer suppresses the notice), so a
// "web" tag here is necessarily a different browser tab on the same host.
function resolutionText(by: string): string {
  if (by === "web") return "answered in another browser tab";
  return `answered on ${surfaceLabel(by)}`;
}

export function createConversationStore(client: Client<typeof HostService>): ConversationStore {
  const [turns, setTurns] = createSignal<Turn[]>([]);
  const [streaming, setStreaming] = createSignal("");
  const [activity, setActivity] = createSignal("");
  const [status, setStatus] = createSignal<Status>({ model: "", session: "" });
  const [ask, setAsk] = createSignal<PendingAsk | null>(null);
  const [resolved, setResolved] = createSignal<ResolvedNotice | null>(null);
  const [fromOther, setFromOther] = createSignal(false);
  const [error, setError] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  // answeredByMe records ask offsets this surface responded to, so a resolved
  // frame naming "web" can tell this tab's own answer apart from another tab's.
  const answeredByMe = new Set<number>();
  // activeAskId is the offset of the ask currently in play. It is kept after an
  // optimistic retract so a late resolved frame (another surface won the race)
  // still matches and shows the correct receipt.
  let activeAskId: number | null = null;
  // pendingLocalTurns counts turns this surface submitted whose turn-begin has
  // not yet arrived over Watch. A turn-begin with none pending came from another
  // surface. Best-effort: two surfaces submitting within the same instant can
  // mis-attribute one marker, which is cosmetic (this is a subtle origin badge,
  // not a correctness barrier — the ask barrier is what serializes control).
  let pendingLocalTurns = 0;

  const push = (t: Turn) => setTurns((prev) => [...prev, t]);

  // commitAssistant flushes the in-progress assistant text into a committed turn,
  // tagging it with the surface it came from when the turn was not local.
  const commitAssistant = () => {
    const text = streaming().trim();
    const other = fromOther();
    setStreaming("");
    setActivity("");
    setFromOther(false);
    if (text) push({ role: "assistant", text, ...(other ? { from: "another surface" } : {}) });
  };

  const ingest = (ev: HostEvent): void => {
    switch (ev.Kind) {
      case HostEventKind.RunnerEvent: {
        const re = ev.RunnerEvent;
        if (!re) return;
        switch (re.kind) {
          case EventKind.TurnBegin:
            // A turn-begin this surface did not submit is a turn from elsewhere;
            // mark the streaming turn so the transcript shows where it came from.
            if (pendingLocalTurns > 0) {
              pendingLocalTurns--;
              setFromOther(false);
            } else {
              setFromOther(true);
            }
            break;
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
      case HostEventKind.ElicitRequest: {
        const id = ev.AskID ?? 0;
        activeAskId = id;
        setResolved(null); // a fresh ask supersedes any prior receipt
        setAsk({ id, message: ev.Elicit?.message ?? "(approval requested)" });
        break;
      }
      case HostEventKind.ElicitResolved: {
        const id = ev.AskID ?? 0;
        // Ignore a resolved frame for an ask this surface never tracked.
        if (id !== activeAskId && !answeredByMe.has(id)) return;
        const by = ev.By ?? "";
        const answeredHere = by === "web" && answeredByMe.has(id);
        setAsk(null); // retract everywhere the moment any surface answered
        // Show a receipt only when someone else answered; a self-answer just
        // retracts. This also settles the race where this tab clicked but
        // another surface won first (by names the winner, not this tab).
        setResolved(answeredHere ? null : { by, text: resolutionText(by) });
        answeredByMe.delete(id);
        activeAskId = null;
        break;
      }
    }
  };

  const submit = async (input: string): Promise<void> => {
    const text = input.trim();
    if (!text || busy()) return;
    setError("");
    setResolved(null);
    push({ role: "user", text });
    pendingLocalTurns++; // this surface expects the next turn-begin
    setBusy(true);
    try {
      await client.submit({ input: text });
    } catch (e) {
      // The turn never started on the host, so no turn-begin will arrive for it;
      // release the pending-turn credit so a later remote turn is not mislabeled.
      pendingLocalTurns = Math.max(0, pendingLocalTurns - 1);
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
    answeredByMe.add(pending.id); // remember so the resolved frame reads as self
    setAsk(null); // optimistic retract; the elicit-resolved frame confirms it
    const result = new TextEncoder().encode(JSON.stringify({ action }));
    try {
      await client.respondToAsk({ askId: BigInt(pending.id), result, by: "web" });
    } catch {
      // Another surface won the ask first (ErrAlreadyResolved) — the resolved
      // frame that named the winner will replace the optimistic retract with the
      // correct receipt, so nothing to do here.
    }
  };

  return {
    turns,
    streaming,
    activity,
    status,
    ask,
    resolved,
    fromOther,
    error,
    busy,
    ingest,
    submit,
    respondToAsk,
    dismissResolved: () => setResolved(null),
    setStatus: (s) => setStatus(s),
  };
}

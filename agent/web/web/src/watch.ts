import type { Client } from "@connectrpc/connect";
import type { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";
import type { HostEvent } from "./hostevent.js";

// FrameLike is the minimal Frame shape decodeFrame needs, so the decoder is
// unit-testable without a Connect stream (kind + the payload bytes).
export interface FrameLike {
  kind: string;
  payload: Uint8Array;
}

// decodeFrame turns one Watch Frame into a HostEvent, or null for the ready
// sentinel (empty kind, emitted so a client attaching to an idle session does
// not block on the first Send) and for any frame whose payload does not decode.
// The stream must never fail on one malformed frame — it skips it.
export function decodeFrame(frame: FrameLike): HostEvent | null {
  if (!frame.kind) return null;
  if (!frame.payload || frame.payload.length === 0) {
    // A kinded frame with no payload still tells the client the event happened.
    return { Kind: frame.kind };
  }
  try {
    return JSON.parse(new TextDecoder().decode(frame.payload)) as HostEvent;
  } catch {
    return { Kind: frame.kind };
  }
}

// WatchStream subscribes onto the host event log (HostService.Watch): the
// retained backlog replayed from offset 0, then live events. It decodes each
// Frame to a HostEvent and hands it to onEvent. On the stream ending or erroring
// it re-subscribes with capped exponential backoff; because Watch always replays
// from offset 0 a fresh subscription is safe, so no cursor is threaded here (the
// consumer is expected to be idempotent over a replay, like the Conversation
// store rebuilding from turn-done). Mirrors diffpp's RelayRoom reconnect.
export class WatchStream {
  private controller: AbortController | null = null;
  private closed = false;
  private retry = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly client: Client<typeof HostService>,
    private readonly onEvent: (ev: HostEvent) => void,
    private readonly onOpen: () => void = () => {},
  ) {}

  start(): void {
    void this.run();
  }

  private async run(): Promise<void> {
    if (this.closed) return;
    this.controller = new AbortController();
    try {
      const iter = this.client.watch({}, { signal: this.controller.signal });
      let first = true;
      for await (const frame of iter) {
        if (first) {
          this.retry = 0;
          this.onOpen();
          first = false;
        }
        const ev = decodeFrame(frame);
        if (ev) this.onEvent(ev);
      }
    } catch {
      // A drop, a network error, or an abort all land here; reconnect unless we
      // were stopped.
    }
    this.scheduleReconnect();
  }

  private scheduleReconnect(): void {
    if (this.closed || this.timer) return;
    const delay = Math.min(500 * 2 ** this.retry++, 10000);
    this.timer = setTimeout(() => {
      this.timer = null;
      void this.run();
    }, delay);
  }

  stop(): void {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    this.controller?.abort();
  }
}

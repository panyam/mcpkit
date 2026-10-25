import { base64Encode } from "@bufbuild/protobuf/wire";

/** The messages a second a page sends at most by default, matching massrelay.DefaultSendRate. */
export const DEFAULT_SEND_RATE = 25;

// How many times a message the relay dropped for the rate is sent again, and
// how many recent messages are kept so one can be. The relay's limit is per
// second, so the first resend waits a second.
const MAX_RESENDS = 3;
const RESEND_BASE_MS = 1000;
const KEEP_SENT = 256;

/** The action id a relay `RATE_LIMITED` message names, as in `Dropped action "a1": over 30 messages/sec`. */
export function droppedAction(message: string): string | undefined {
  const m = /^Dropped action ("(?:[^"\\]|\\.)*")/.exec(message);
  if (!m) return undefined;
  try {
    return JSON.parse(m[1]);
  } catch {
    return undefined;
  }
}

interface Action {
  actionId: string;
  appMessage: { kind: string; payload: string; toClientIds: string[] };
}

/**
 * Sends a page's AppMessages at most `rate` a second, in order, and sends
 * again any the relay reports it dropped for the rate. Each message carries
 * its own action id, which is how a `RATE_LIMITED` reply names it.
 */
export class Sender {
  private queue: Action[] = [];
  private tokens: number;
  private last = Date.now();
  private timer?: ReturnType<typeof setTimeout>;
  private resends = new Set<ReturnType<typeof setTimeout>>();
  private sent = new Map<string, { action: Action; attempts: number }>();
  private seq = 0;
  private stopped = false;

  constructor(
    private write: (action: Action) => void,
    private rate: number,
  ) {
    this.tokens = this.burst();
  }

  send(kind: string, payload: Uint8Array, to: string): void {
    if (this.stopped) return;
    const action = { actionId: `p${++this.seq}`, appMessage: { kind, payload: base64Encode(payload), toClientIds: [to] } };
    this.sent.set(action.actionId, { action, attempts: 0 });
    if (this.sent.size > KEEP_SENT) this.sent.delete(this.sent.keys().next().value!);
    this.enqueue(action);
  }

  /** Handles a relay `RATE_LIMITED` error. Reports whether it named a message this page sent. */
  rateLimited(message: string): boolean {
    const id = droppedAction(message);
    const entry = id === undefined ? undefined : this.sent.get(id);
    if (!entry || this.stopped) return false;
    if (entry.attempts >= MAX_RESENDS) {
      this.sent.delete(id!);
      console.warn(`mcpkit-rooms: the relay dropped message ${id} for the rate ${MAX_RESENDS + 1} times; giving up on it`);
      return true;
    }
    const t = setTimeout(() => {
      this.resends.delete(t);
      this.enqueue(entry.action);
    }, RESEND_BASE_MS * 2 ** entry.attempts++);
    this.resends.add(t);
    return true;
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.timer);
    for (const t of this.resends) clearTimeout(t);
    this.resends.clear();
    this.queue = [];
    this.sent.clear();
  }

  private burst(): number {
    return Math.max(1, Math.floor(this.rate));
  }

  private enqueue(action: Action): void {
    this.queue.push(action);
    this.drain();
  }

  private drain(): void {
    if (this.timer !== undefined || this.stopped) return;
    if (this.rate <= 0) {
      for (const a of this.queue.splice(0)) this.write(a);
      return;
    }
    const now = Date.now();
    this.tokens = Math.min(this.burst(), this.tokens + ((now - this.last) * this.rate) / 1000);
    this.last = now;
    while (this.queue.length > 0 && this.tokens >= 1) {
      this.tokens--;
      this.write(this.queue.shift()!);
    }
    if (this.queue.length > 0) {
      this.timer = setTimeout(() => {
        this.timer = undefined;
        this.drain();
      }, Math.ceil(((1 - this.tokens) * 1000) / this.rate));
    }
  }
}

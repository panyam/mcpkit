import { joinToolRoom, type JoinToolRoomOptions, type ToolRoom } from "./room.js";

/** What the host's join endpoint answers (Go: `rooms.JoinResponse`). */
export interface JoinResponse {
  /** The one room the token admits the page to. */
  room: string;
  /** The relay's base URL, possibly relative to the endpoint. */
  relayUrl: string;
  /** The relay token for the page's user. */
  token: string;
  /** The relay subjects of the host's bridges, for `trustBridges`. */
  bridgeSubjects: string[];
  /** When the token expires, as an RFC 3339 time. */
  expiresAt: string;
}

/** Options for {@link joinToolRoomFrom}. */
export type JoinToolRoomFromOptions = Omit<JoinToolRoomOptions, "relay" | "room" | "token" | "getToken" | "trustBridges"> & {
  /**
   * Request options for each call to the endpoint, such as a body naming the
   * document or extra headers. `method` is always POST. `credentials`
   * defaults to `same-origin`, so the host's session cookie goes along.
   */
  init?: RequestInit;
  /** A fetch implementation, for runtimes without a global one. */
  fetch?: typeof fetch;
};

/**
 * Asks the host's join endpoint for a room and a relay token, then joins it
 * with {@link joinToolRoom}. The page trusts only the bridges the endpoint
 * names. Before the token expires the page calls the endpoint again and hands
 * the relay the new token over the open connection, so it keeps its id.
 *
 * The endpoint may be relative to the page, and the relay URL it answers may
 * be relative to the endpoint.
 */
export async function joinToolRoomFrom(endpoint: string, opts: JoinToolRoomFromOptions = {}): Promise<ToolRoom> {
  const { init, fetch: fetchImpl, ...rest } = opts;
  const url = new URL(endpoint, (globalThis as { location?: { href: string } }).location?.href).toString();
  const call = () => fetchJoin(url, fetchImpl ?? globalThis.fetch.bind(globalThis), init);
  const first = await call();
  // The relay client also asks getToken for the token it connects with. The
  // first answer is the token just fetched, so joining costs one request.
  let unused: string | undefined = first.token;
  const getToken = async (): Promise<string> => {
    if (unused !== undefined) {
      const t = unused;
      unused = undefined;
      return t;
    }
    const next = await call();
    if (next.room !== first.room) {
      throw new Error(`joinToolRoomFrom: the endpoint moved the page from ${first.room} to ${next.room}`);
    }
    return next.token;
  };
  return joinToolRoom({
    ...rest,
    relay: new URL(first.relayUrl, url).toString(),
    room: first.room,
    token: first.token,
    getToken,
    trustBridges: first.bridgeSubjects?.length ? first.bridgeSubjects : undefined,
  });
}

/** POSTs to a join endpoint and returns its answer, or throws with the endpoint's error. */
export async function fetchJoin(url: string, fetchImpl: typeof fetch, init: RequestInit = {}): Promise<JoinResponse> {
  const res = await fetchImpl(url, { credentials: "same-origin", ...init, method: "POST" });
  const text = await res.text();
  let body: any;
  try {
    body = JSON.parse(text);
  } catch {
    body = undefined;
  }
  if (!res.ok) {
    throw new Error(`joinToolRoomFrom: ${url}: ${res.status} ${body?.error ?? text}`);
  }
  if (!body || typeof body.room !== "string" || typeof body.token !== "string" || typeof body.relayUrl !== "string") {
    throw new Error(`joinToolRoomFrom: ${url}: not a join response: ${text.slice(0, 200)}`);
  }
  return body as JoinResponse;
}

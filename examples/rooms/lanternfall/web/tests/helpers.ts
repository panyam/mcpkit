import { expect, type Browser, type Page } from "@playwright/test";
import { Client, StreamableHTTPClientTransport } from "@modelcontextprotocol/client";
import { PORT } from "../playwright.config.js";

export const BASE = `http://localhost:${PORT}`;

/** A pass-through Standard Schema, for requests the client has no type for. */
export const anyResult = {
  "~standard": { version: 1 as const, vendor: "lanternfall", validate: (value: unknown) => ({ value }) },
};

/** A fresh game per test, so tests never see each other's pages. */
export function newGame(): string {
  return `t-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
}

/** Opens a player's page in a context of its own (its own cookie) and waits until it is in the room. */
export async function openPlayer(browser: Browser, player: string, game: string): Promise<Page> {
  const ctx = await browser.newContext({ baseURL: BASE });
  const page = await ctx.newPage();
  await page.goto(`/signin?as=${player}&game=${game}`);
  await expect(page.locator("body")).toHaveAttribute("data-ready", "true", { timeout: 15_000 });
  return page;
}

export async function pageId(page: Page): Promise<string> {
  return page.evaluate(() => (window as any).lanternfall.room.id);
}

/** An MCP session acting for player, as any agent would connect. */
export async function connectAgent(player: string): Promise<Client> {
  const client = new Client({ name: `agent-${player}`, version: "0" });
  await client.connect(
    new StreamableHTTPClientTransport(new URL(`${BASE}/mcp`), {
      requestInit: { headers: { Authorization: `Bearer player:${player}` } },
    }),
  );
  return client;
}

export function textOf(res: any): string {
  return (res.content ?? []).map((c: any) => c.text ?? "").join(" ");
}

/** Waits for the next event named name through next_request, skipping others. */
export async function nextEvent(agent: Client, resource: string, name: string, after?: string): Promise<{ event: any; cursor: string }> {
  const deadline = Date.now() + 15_000;
  let cursor = after;
  while (Date.now() < deadline) {
    const res: any = await agent.callTool({
      name: "next_request",
      arguments: { resource, wait_seconds: 5, ...(cursor ? { after: cursor } : {}) },
    });
    const out = res.structuredContent ?? {};
    cursor = out.cursor ?? cursor;
    if (out.event?.name === name) return { event: out.event, cursor: cursor! };
  }
  throw new Error(`no ${name} event within 15s`);
}

/** The cursor to wait from: whatever happens after this call. */
export async function eventCursor(agent: Client, resource: string): Promise<string> {
  const res: any = await agent.callTool({ name: "next_request", arguments: { resource, wait_seconds: 0 } });
  return res.structuredContent?.cursor;
}

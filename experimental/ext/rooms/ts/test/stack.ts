import { spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { Client, StreamableHTTPClientTransport } from "@modelcontextprotocol/client";

/** What the Go teststack prints: a relay, the bridge's MCP endpoint, tokens. */
export interface StackInfo {
  relay: string;
  mcp: string;
  pageTokens: Record<string, string>;
  agentTokens: Record<string, string>;
}

export const ROOM = "design:demo/board.kicad_sch";

// Resolved from the path string: under jsdom the global URL is not the one
// fileURLToPath accepts.
const MODULE_DIR = join(dirname(fileURLToPath(import.meta.url)), "../../massrelay");

/**
 * Starts the Go room bridge on a real massrelay relay (go run of
 * experimental/ext/rooms/massrelay/internal/cmd/teststack) and resolves with
 * its URLs once it is serving. Closing its stdin stops it.
 */
export async function startStack(args: string[] = []): Promise<{ info: StackInfo; stop(): void }> {
  const proc: ChildProcess = spawn("go", ["run", "./internal/cmd/teststack", ...args], {
    cwd: MODULE_DIR,
    stdio: ["pipe", "pipe", "inherit"],
  });
  const lines = createInterface({ input: proc.stdout! });
  const info = await new Promise<StackInfo>((resolve, reject) => {
    proc.once("exit", (code) => reject(new Error(`teststack exited with ${code} before serving`)));
    lines.once("line", (line) => resolve(JSON.parse(line)));
  });
  return {
    info,
    stop() {
      proc.stdin?.end();
      proc.kill();
    },
  };
}

/** An MCP agent session on the bridge, acting for subject. */
export async function connectAgent(info: StackInfo, subject: string): Promise<Client> {
  const client = new Client({ name: `agent-${subject}`, version: "0" });
  const token = info.agentTokens[subject];
  const transport = new StreamableHTTPClientTransport(new URL(info.mcp), {
    authProvider: { token: async () => token },
  });
  await client.connect(transport);
  return client;
}

/** The text of a tool result's content blocks, joined. */
export function textOf(res: any): string {
  return (res.content ?? []).map((c: any) => c.text ?? "").join(" ");
}

/** list_pages as the agent sees it. */
export async function listPages(agent: Client): Promise<any[]> {
  const res: any = await agent.callTool({ name: "list_pages", arguments: {} });
  return res.structuredContent?.pages ?? [];
}

/** Polls until ok returns true, or fails after timeoutMs. */
export async function eventually(ok: () => Promise<boolean> | boolean, timeoutMs = 5000, what = "condition"): Promise<void> {
  const start = Date.now();
  while (!(await ok())) {
    if (Date.now() - start > timeoutMs) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

/** Waits until list_pages shows page agreed to this agent and its default target. */
export async function waitPaired(agent: Client, page: string): Promise<void> {
  await eventually(async () => (await listPages(agent)).some((p) => p.page === page && p.allowed && p.default), 5000, `page ${page} to agree`);
}

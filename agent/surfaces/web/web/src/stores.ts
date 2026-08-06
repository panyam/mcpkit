import type { Client } from "@connectrpc/connect";
import type { HostService } from "./gen/mcpkit/agentweb/v1/host_pb.js";
import type { HostEvent } from "./hostevent.js";
import { createConversationStore, type ConversationStore } from "./conversation.js";
import { createSubAgentStore, type SubAgentStore } from "./subagents.js";
import { createTimelineStore, type TimelineStore } from "./timeline.js";
import { createMemoryStore, type MemoryStore } from "./memory.js";
import { createToolStore, type ToolStore } from "./tools.js";
import { createBudgetStore, type BudgetStore } from "./budget.js";

// PanelStores is the bundle of per-panel projections behind the workspace. The
// #1197 shell fed one ConversationStore off the Watch stream; #1198 adds the
// observability panels, each its own projection of the same stream. A single
// WatchStream feeds bundle.ingest, which fans one HostEvent to every store, so a
// turn streamed once lands in whichever panels are open (the same idempotent-
// over-replay contract the Conversation store already honors). The memory store
// also holds the client for its on-demand /memory read.
export interface PanelStores {
  conversation: ConversationStore;
  subAgents: SubAgentStore;
  timeline: TimelineStore;
  memory: MemoryStore;
  tools: ToolStore;
  budget: BudgetStore;
  ingest: (ev: HostEvent) => void;
}

export function createPanelStores(client: Client<typeof HostService>): PanelStores {
  const conversation = createConversationStore(client);
  const subAgents = createSubAgentStore();
  const timeline = createTimelineStore();
  const memory = createMemoryStore(client);
  const tools = createToolStore();
  const budget = createBudgetStore();

  const sinks = [conversation, subAgents, timeline, memory, tools, budget];

  const ingest = (ev: HostEvent): void => {
    for (const sink of sinks) sink.ingest(ev);
  };

  return { conversation, subAgents, timeline, memory, tools, budget, ingest };
}

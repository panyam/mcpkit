# Tasks (v1 / v2 / hybrid)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** root · **Prerequisites:** [request-anatomy](./request-anatomy.md), [notifications](./notifications.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [request-anatomy](./request-anatomy.md) Next-to-read, [notifications](./notifications.md) Next-to-read, [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Branches into:** [cancellation](./cancellation.md)
> **Spec:** [SEP-2663 (tasks v2)](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/task.go`, `core/task_v2.go`, `server/tasks_v1.go`, `server/tasks_v2.go`, `server/tasks_hybrid.go`, `server/task_store.go`, `server/task_session.go`, `server/task_callbacks.go`, `server/task_queue.go`

## Prerequisites

- You know how a regular request flows through dispatch + middleware + handler. → If not, read [request-anatomy](./request-anatomy.md).
- You know how progress notifications work and how `progressToken` pairs them with originating requests. → If not, read [notifications](./notifications.md).
- You know what an extension is and how SEP-2663 fits in. → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

Some operations don't fit the single-request → single-response model. They run for minutes, need to be queryable, must survive transport drops, may be cancelled by either side. **Tasks** is MCP's first-class long-running-operation primitive (SEP-2663). This page covers the v1 / v2 / hybrid coexistence pattern in mcpkit, the task store, detach/resume semantics, and how progress notifications integrate.

## What this page will cover

- The task lifecycle states (created, running, completed, failed, cancelled)
- v1 (frozen, original SEP) vs. v2 (canonical, SEP-2663) on-the-wire shape differences
- mcpkit's three entry points: `RegisterTasksV1`, `RegisterTasks`, `RegisterTasksHybrid` — the hybrid dispatches by negotiated capability
- The task store — what's persisted, restart durability, query semantics
- Detach/resume — how a task can outlive the originating request
- Progress notifications integration: `progressToken` pairing for tasks
- Cancellation across task lifecycle (`notifications/cancelled` vs task-level cancel)
- The migration shape: register both v1 and v2 handlers, pick at dispatch time

## Next to read

- **[Cancellation deep-dive](./cancellation.md)** *(stub leaf)* — race scenarios specific to long-running operations.

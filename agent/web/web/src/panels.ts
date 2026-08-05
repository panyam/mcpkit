// PanelId is the shared identity of a workspace panel. The shell references
// panels only by id; the DockView variant resolves where each lives. This slice
// ships one panel (conversation); #1198 adds the observability panels
// (sub-agent tree, activity timeline, memory, tool/offload, budgets) by
// extending this union and the registry in dock.ts — the saved-layout reconcile
// makes a newly added panel appear without disturbing existing arrangements.
export type PanelId = "conversation";

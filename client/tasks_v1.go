package client

// V1 task client (MCP spec 2025-11-25, pre-SEP-2663). Frozen alongside the
// server-side v1 path so existing v1 conformance / unit tests stay green.
// New code should target the v2 helpers in client/tasks.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
)

// --- Typed request params (v1 wire) ---

type getTaskParamsV1 struct {
	TaskID string `json:"taskId"`
}

type resultParamsV1 struct {
	TaskID string `json:"taskId"`
}

type listTasksParamsV1 struct {
	Cursor string `json:"cursor,omitempty"`
}

type cancelTaskParamsV1 struct {
	TaskID string `json:"taskId"`
}

// toolCallAsTaskParamsV1 is the wire-format params for tools/call with a v1
// task hint. Per MCP spec 2025-11-25: task hint is at params.task (sibling of
// name/arguments).
type toolCallAsTaskParamsV1 struct {
	Name      string         `json:"name"`
	Arguments any            `json:"arguments"`
	Task      taskParamV1    `json:"task"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

type taskParamV1 struct {
	TTL          int `json:"ttl,omitempty"`          // milliseconds
	PollInterval int `json:"pollInterval,omitempty"` // milliseconds
}

// TaskCallOptionsV1 configures a ToolCallAsTaskV1 invocation.
// Nil means use server defaults for everything.
type TaskCallOptionsV1 struct {
	// TTL in milliseconds. 0 = server default.
	TTL int

	// PollInterval in milliseconds. 0 = server default.
	PollInterval int

	// ProgressToken is passed as _meta.progressToken so the server
	// echoes it in notifications/progress. Nil = no token.
	ProgressToken any
}

// IsToolTaskV1 checks whether a tool supports task invocation based on its
// Execution.TaskSupport field. Returns true for "required" or "optional".
// Per MCP spec 2025-11-25: absent Execution = forbidden. (V2 servers decide
// task creation unilaterally — there is no equivalent client-side check.)
//
// Use with ListTools to decide whether to call ToolCallAsTaskV1 or ToolCall:
//
//	tools, _ := c.ListTools()
//	for _, t := range tools {
//	    if client.IsToolTaskV1(t) {
//	        client.ToolCallAsTaskV1(c, t.Name, args)
//	    } else {
//	        c.ToolCall(t.Name, args)
//	    }
//	}
func IsToolTaskV1(tool core.ToolDef) bool {
	return tool.Execution != nil &&
		tool.Execution.TaskSupport != core.TaskSupportForbidden
}

// --- v1 client helpers ---

// GetTaskV1 polls the status of a v1 task by ID. Non-blocking.
// Per MCP spec 2025-11-25 §tasks/get: returns flat Result & Task.
func GetTaskV1(c *Client, taskID string) (*core.GetTaskResultV1, error) {
	result, err := c.Call("tasks/get", getTaskParamsV1{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var r core.GetTaskResultV1
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/get: %w", err)
	}
	return &r, nil
}

// GetTaskPayloadV1 fetches the result payload for a v1 task. Blocks until
// the task reaches a terminal state via the tasks/result long-poll.
// Per MCP spec 2025-11-25 §tasks/result: returns the original ToolResult
// with _meta["io.modelcontextprotocol/related-task"]. (V2 removed
// tasks/result — DetailedTask inlines the result instead.)
func GetTaskPayloadV1(c *Client, taskID string) (*core.ToolResult, string, error) {
	result, err := c.Call("tasks/result", resultParamsV1{TaskID: taskID})
	if err != nil {
		return nil, "", err
	}
	var r core.ToolResult
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, "", fmt.Errorf("unmarshal tasks/result: %w", err)
	}
	var relatedID string
	if r.Meta != nil && r.Meta.RelatedTask != nil {
		relatedID = r.Meta.RelatedTask.TaskID
	}
	return &r, relatedID, nil
}

// ListTasksV1 returns all v1 tasks with cursor-based pagination.
// Pass an empty cursor to start from the beginning.
// (Removed in v2 — tasks/list is no longer part of the protocol.)
func ListTasksV1(c *Client, cursor string) (*core.ListTasksResultV1, error) {
	result, err := c.Call("tasks/list", listTasksParamsV1{Cursor: cursor})
	if err != nil {
		return nil, err
	}
	var r core.ListTasksResultV1
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/list: %w", err)
	}
	return &r, nil
}

// CancelTaskV1 cancels a running v1 task. Returns an error if the task is
// already in a terminal state.
// Per MCP spec 2025-11-25 §tasks/cancel: returns flat Result & Task.
// (V2 returns an empty ack; the v2 helper signature reflects that.)
func CancelTaskV1(c *Client, taskID string) (*core.CancelTaskResultV1, error) {
	result, err := c.Call("tasks/cancel", cancelTaskParamsV1{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var r core.CancelTaskResultV1
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/cancel: %w", err)
	}
	return &r, nil
}

// ToolCallAsTaskV1 invokes a v1 tool with a task hint, returning a
// CreateTaskResultV1 instead of the immediate tool result. The server
// creates a task and runs the tool asynchronously.
//
// Pass nil for opts to use server defaults. Per MCP spec 2025-11-25:
// task hint at params.task, progressToken at params._meta.progressToken.
// (V2 removes the client task hint — the server decides unilaterally —
// so there is no V2 equivalent to this helper.)
func ToolCallAsTaskV1(c *Client, name string, args any, opts ...*TaskCallOptionsV1) (*core.CreateTaskResultV1, error) {
	params := toolCallAsTaskParamsV1{
		Name:      name,
		Arguments: args,
	}
	if len(opts) > 0 && opts[0] != nil {
		o := opts[0]
		params.Task = taskParamV1{TTL: o.TTL, PollInterval: o.PollInterval}
		if o.ProgressToken != nil {
			params.Meta = map[string]any{"progressToken": o.ProgressToken}
		}
	}
	result, err := c.Call("tools/call", params)
	if err != nil {
		return nil, err
	}
	var r core.CreateTaskResultV1
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal task creation: %w", err)
	}
	return &r, nil
}

// WaitForTaskV1 polls tasks/get until the v1 task reaches a terminal state
// or the context is cancelled. Returns the final task info.
//
// Use pollInterval of 0 for a 1-second default. The context controls
// the overall timeout — use context.WithTimeout for deadline-based waiting.
func WaitForTaskV1(ctx context.Context, c *Client, taskID string, pollInterval time.Duration) (*core.GetTaskResultV1, error) {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}
	for {
		got, err := GetTaskV1(c, taskID)
		if err != nil {
			return nil, err
		}
		if got.Status.IsTerminal() {
			return got, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
)

// --- Typed request params ---

type getTaskParams struct {
	TaskID string `json:"taskId"`
}

type resultParams struct {
	TaskID string `json:"taskId"`
}

type listTasksParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type cancelTaskParams struct {
	TaskID string `json:"taskId"`
}

// toolCallAsTaskParams is the wire-format params for tools/call with a task hint.
// Per MCP spec 2025-11-25: task hint is at params.task (sibling of name/arguments).
type toolCallAsTaskParams struct {
	Name      string         `json:"name"`
	Arguments any            `json:"arguments"`
	Task      taskParam      `json:"task"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

type taskParam struct {
	TTL          int `json:"ttl,omitempty"`          // milliseconds
	PollInterval int `json:"pollInterval,omitempty"` // milliseconds
}

// TaskCallOptions configures a ToolCallAsTask invocation.
// Nil means use server defaults for everything.
type TaskCallOptions struct {
	// TTL in milliseconds. 0 = server default.
	TTL int

	// PollInterval in milliseconds. 0 = server default.
	PollInterval int

	// ProgressToken is passed as _meta.progressToken so the server
	// echoes it in notifications/progress. Nil = no token.
	ProgressToken any
}

// --- Client helpers ---

// GetTask polls the status of a task by ID. Non-blocking.
// Per MCP spec 2025-11-25 §tasks/get: returns flat Result & Task.
func GetTask(c *Client, taskID string) (*core.GetTaskResult, error) {
	result, err := c.Call("tasks/get", getTaskParams{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var r core.GetTaskResult
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/get: %w", err)
	}
	return &r, nil
}

// GetTaskPayload fetches the result payload for a task. Blocks until the
// task reaches a terminal state via the tasks/result long-poll.
// Per MCP spec 2025-11-25 §tasks/result: returns the original ToolResult
// with _meta["io.modelcontextprotocol/related-task"].
func GetTaskPayload(c *Client, taskID string) (*core.ToolResult, string, error) {
	result, err := c.Call("tasks/result", resultParams{TaskID: taskID})
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

// ListTasks returns all tasks with cursor-based pagination.
// Pass an empty cursor to start from the beginning.
func ListTasks(c *Client, cursor string) (*core.ListTasksResult, error) {
	result, err := c.Call("tasks/list", listTasksParams{Cursor: cursor})
	if err != nil {
		return nil, err
	}
	var r core.ListTasksResult
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/list: %w", err)
	}
	return &r, nil
}

// CancelTask cancels a running task. Returns an error if the task is
// already in a terminal state.
// Per MCP spec 2025-11-25 §tasks/cancel: returns flat Result & Task.
func CancelTask(c *Client, taskID string) (*core.CancelTaskResult, error) {
	result, err := c.Call("tasks/cancel", cancelTaskParams{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var r core.CancelTaskResult
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal tasks/cancel: %w", err)
	}
	return &r, nil
}

// ToolCallAsTask invokes a tool with a task hint, returning a CreateTaskResult
// instead of the immediate tool result. The server creates a task and runs
// the tool asynchronously.
//
// Pass nil for opts to use server defaults. Per MCP spec 2025-11-25:
// task hint at params.task, progressToken at params._meta.progressToken.
func ToolCallAsTask(c *Client, name string, args any, opts ...*TaskCallOptions) (*core.CreateTaskResult, error) {
	params := toolCallAsTaskParams{
		Name:      name,
		Arguments: args,
	}
	if len(opts) > 0 && opts[0] != nil {
		o := opts[0]
		params.Task = taskParam{TTL: o.TTL, PollInterval: o.PollInterval}
		if o.ProgressToken != nil {
			params.Meta = map[string]any{"progressToken": o.ProgressToken}
		}
	}
	result, err := c.Call("tools/call", params)
	if err != nil {
		return nil, err
	}
	var r core.CreateTaskResult
	if err := json.Unmarshal(result.Raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal task creation: %w", err)
	}
	return &r, nil
}

// WaitForTask polls tasks/get until the task reaches a terminal state.
// Returns the final task info. Use pollInterval of 0 for the server's
// suggested interval (from CreateTaskResult.PollInterval), or provide
// a custom interval.
//
// This is a convenience wrapper around GetTask for the common pattern
// of polling until completion.
func WaitForTask(c *Client, taskID string, pollInterval time.Duration) (*core.GetTaskResult, error) {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}
	for {
		got, err := GetTask(c, taskID)
		if err != nil {
			return nil, err
		}
		if got.Status.IsTerminal() {
			return got, nil
		}
		time.Sleep(pollInterval)
	}
}

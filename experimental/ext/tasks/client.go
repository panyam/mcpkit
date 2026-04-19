package tasks

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// --- Typed request params ---

// getTaskParams is the wire-format params for tasks/get.
type getTaskParams struct {
	TaskID string `json:"taskId"`
}

// resultParams is the wire-format params for tasks/result.
type resultParams struct {
	TaskID string `json:"taskId"`
}

// listTasksParams is the wire-format params for tasks/list.
type listTasksParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// cancelTaskParams is the wire-format params for tasks/cancel.
type cancelTaskParams struct {
	TaskID string `json:"taskId"`
}

// toolCallAsTaskParams is the wire-format params for tools/call with a task hint.
// Per spec: task hint is at params.task (sibling of name/arguments), NOT _meta.task.
type toolCallAsTaskParams struct {
	Name      string    `json:"name"`
	Arguments any       `json:"arguments"`
	Task      taskParam `json:"task"`
}

type taskParam struct {
	TTL int `json:"ttl,omitempty"` // milliseconds
}

// --- Client helpers ---

// GetTask polls the status of a task by ID. Non-blocking.
// Per spec: tasks/get returns flat Result & Task — fields at root level.
func GetTask(c *client.Client, taskID string) (*core.GetTaskResult, error) {
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
// task reaches a terminal state. Per spec: returns the original ToolResult
// with _meta["io.modelcontextprotocol/related-task"]. Returns the ToolResult,
// the related taskId, and any error.
func GetTaskPayload(c *client.Client, taskID string) (*core.ToolResult, string, error) {
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

// ListTasks returns all tasks with cursor-based pagination. Pass an empty
// cursor to start from the beginning.
func ListTasks(c *client.Client, cursor string) (*core.ListTasksResult, error) {
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
// Per spec: tasks/cancel returns flat Result & Task.
func CancelTask(c *client.Client, taskID string) (*core.CancelTaskResult, error) {
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
// the tool asynchronously. Per spec: task hint at params.task.
func ToolCallAsTask(c *client.Client, name string, args any, ttlMs int) (*core.CreateTaskResult, error) {
	params := toolCallAsTaskParams{
		Name:      name,
		Arguments: args,
		Task:      taskParam{TTL: ttlMs},
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

package tasks

import (
	"context"
	"encoding/json"
	"slices"

	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// TaskIDsFilterKey is the subscriptions/listen filter key ext-tasks defines:
// an array of task IDs whose notifications/tasks the client wants on the
// listen stream (specification/2026-07-28/tasks.md, "Task Status
// Notifications"). tasks.Register registers it.
const TaskIDsFilterKey = "taskIds"

// taskIDsFilterKey is the taskIds key for rt. Accept refuses the whole listen
// with -32021 when the caller has not declared the tasks extension, as the
// spec requires, and keeps only the IDs the caller could read with tasks/get,
// so the acknowledgement never confirms another tenant's task. Declining
// (nil, nil) when none survive leaves taskIds out of the acknowledgement.
func taskIDsFilterKey(rt *v2TaskRuntime) server.SubscriptionFilterKey {
	return server.SubscriptionFilterKey{
		Key: TaskIDsFilterKey,
		Accept: func(ctx context.Context, requested json.RawMessage) (*server.SubscriptionFilterGrant, error) {
			if !core.ClientSupportsExtension(ctx, core.TasksExtensionID) {
				return nil, &core.MissingCapabilityError{
					Required: core.ClientCapabilities{Extensions: map[string]core.ClientExtensionCap{core.TasksExtensionID: {}}},
					Message:  "Missing required client capability",
				}
			}
			var ids []string
			if json.Unmarshal(requested, &ids) != nil {
				return nil, nil
			}
			bucket := core.TaskBucketKey(ctx)
			var accepted []string
			for _, id := range ids {
				if _, ok := rt.store.Get(id, bucket); ok && !slices.Contains(accepted, id) {
					accepted = append(accepted, id)
				}
			}
			if len(accepted) == 0 {
				return nil, nil
			}
			return &server.SubscriptionFilterGrant{
				Echo: accepted,
				Match: func(method string, params json.RawMessage) bool {
					if method != "notifications/tasks" {
						return false
					}
					var p struct {
						TaskID string `json:"taskId"`
					}
					return json.Unmarshal(params, &p) == nil && slices.Contains(accepted, p.TaskID)
				},
			}, nil
		},
	}
}

// publishTaskStatus sends one notifications/tasks payload. notify carries it
// to the session stream, as it always has; listeners carries it to any
// subscriptions/listen stream whose taskIds include the task, which is the
// only way a stateless client hears it (#1523).
func publishTaskStatus(ctx context.Context, notify func(method string, params any), listeners func(context.Context, string, any), payload core.DetailedTask) {
	notify("notifications/tasks", payload)
	if listeners != nil {
		listeners(ctx, "notifications/tasks", payload)
	}
}

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// onTaskDetach registers the handle and tells the user their job moved to
// the background.
func (a *App) onTaskDetach(bt *client.BackgroundTask) {
	a.tasksMu.Lock()
	a.bgTasks[bt.TaskID] = bt
	a.tasksMu.Unlock()
	a.renderer.taskDetached(bt)
}

// onTaskComplete runs on the background poll goroutine: it surfaces the
// outcome as a transcript line, feeds a task.completed event into the
// injection policy (so the next turn carries the result as context), and
// gives the trigger policy a shot at a proactive turn (a host that wants
// "tell the user immediately" binds a trigger on task.completed; nothing is
// hardcoded, so N finishing tasks cannot nag by default).
func (a *App) onTaskComplete(serverID string, bt *client.BackgroundTask) {
	a.tasksMu.Lock()
	delete(a.bgTasks, bt.TaskID)
	a.tasksMu.Unlock()
	a.renderer.taskCompleted(bt)

	dt, err := bt.Result()
	payload := map[string]any{"taskId": bt.TaskID, "tool": bt.Tool}
	switch {
	case err != nil:
		payload["error"] = err.Error()
	case dt != nil && dt.Status == core.TaskFailed && dt.Error != nil:
		payload["error"] = dt.Error.Message
	case dt != nil && dt.Result != nil:
		payload["result"] = resultText(dt.Result)
		payload["isError"] = dt.Result.IsError
	}
	raw, _ := json.Marshal(payload)
	ev := agent.IncomingEvent{
		Server: serverID,
		Name:   "task.completed",
		ID:     bt.TaskID,
		Time:   time.Now(),
		Data:   core.NewRawJSON(raw),
	}
	a.injection.Ingest(ev)
	if firing := a.triggers.OnEvent(ev); firing != nil {
		a.runProactiveTurn(context.Background(), firing)
	}
}

// snapshotTasks lists running background tasks for /tasks.
func (a *App) snapshotTasks() []*client.BackgroundTask {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	out := make([]*client.BackgroundTask, 0, len(a.bgTasks))
	for _, bt := range a.bgTasks {
		out = append(out, bt)
	}
	return out
}

// cancelTask services "/tasks cancel <id>".
func (a *App) cancelTask(id string) {
	a.tasksMu.Lock()
	bt := a.bgTasks[id]
	a.tasksMu.Unlock()
	if bt == nil {
		fmt.Fprintf(a.renderer.out, "no running task %q (see /tasks)\n", id)
		return
	}
	if err := bt.Cancel(); err != nil {
		a.renderer.turnFailed(err)
	}
}

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// onTaskDetach registers the handle and tells the user their job moved to
// the background.
func (a *App) onTaskDetach(bt *client.BackgroundTask) {
	a.tasks.add(bt)
	a.emit(HostEvent{Kind: HostTaskDetached, Task: bt})
}

// onTaskComplete runs on the background poll goroutine: it surfaces the
// outcome as a transcript line, feeds a task.completed event into the
// injection policy (so the next turn carries the result as context), and
// gives the trigger policy a shot at a proactive turn (a host that wants
// "tell the user immediately" binds a trigger on task.completed; nothing is
// hardcoded, so N finishing tasks cannot nag by default).
func (a *App) onTaskComplete(serverID string, bt *client.BackgroundTask) {
	a.tasks.remove(bt.TaskID)
	a.emit(HostEvent{Kind: HostTaskCompleted, Task: bt})

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
	return a.tasks.all()
}

// cancelTask services "/tasks cancel <id>".
func (a *App) cancelTask(id string) {
	bt := a.tasks.get(id)
	if bt == nil {
		a.emit(HostEvent{Kind: HostMessage, Message: fmt.Sprintf("no running task %q (see /tasks)", id)})
		return
	}
	if err := bt.Cancel(context.Background()); err != nil {
		a.emit(HostEvent{Kind: HostTurnFailed, Err: err.Error()})
	}
}

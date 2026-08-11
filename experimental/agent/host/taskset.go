package host

import (
	"sync"

	"github.com/panyam/mcpkit/client"
)

// taskSet tracks the background tasks a turn detached, keyed by task id.
//
// It owns its own mutex rather than borrowing the App's, because the
// membership it guards is unrelated to a turn: a completion poll goroutine
// removes an entry while a turn is running, and /tasks reads the set between
// turns. Keeping the lock here also keeps its scope honest — every path that
// touches the map goes through a method, so there is no site that reads it
// unlocked.
type taskSet struct {
	mu sync.Mutex
	m  map[string]*client.BackgroundTask
}

func newTaskSet() *taskSet {
	return &taskSet{m: map[string]*client.BackgroundTask{}}
}

// add registers a task that has detached to the background.
func (t *taskSet) add(bt *client.BackgroundTask) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[bt.TaskID] = bt
}

// remove drops a task that has finished. Removing an unknown id is a no-op,
// so a completion racing a cancel is safe.
func (t *taskSet) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// get returns the task, or nil when it is unknown — already finished, or
// never detached.
func (t *taskSet) get(id string) *client.BackgroundTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[id]
}

// all snapshots the running tasks. The slice is the caller's; the handles in
// it are shared and may finish while the caller reads them.
func (t *taskSet) all() []*client.BackgroundTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*client.BackgroundTask, 0, len(t.m))
	for _, bt := range t.m {
		out = append(out, bt)
	}
	return out
}

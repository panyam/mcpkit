package skills

import "github.com/fsnotify/fsnotify"

// exportedMapFSNotifyOp surfaces the unexported event-op mapper to the
// skills_test package so its behavior can be unit-tested without
// promoting it to the public API.
func ExportedMapFSNotifyOp(op fsnotify.Op) (ChangeAction, bool) {
	return mapFSNotifyOp(op)
}

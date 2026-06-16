package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultFSWatcherIgnore is the always-on ignore set for the fsnotify
// Detector — directories that almost certainly aren't skill content
// and would generate noisy events. Augmented by WithFSWatcherIgnore.
var defaultFSWatcherIgnore = []string{".git", "node_modules", ".DS_Store"}

// fsWatcher is the fsnotify-driven Detector. It walks hostRoot at
// setup, watches every directory not matched by the ignore set, and
// translates fsnotify events into PathChange events forwarded to
// Provider.NotifyChangedEvents.
//
// Lifecycle is owned by Provider: setup happens in NewProvider (so
// errors surface there), start happens in RegisterWith (so events
// don't fire before a server is bound), Close stops abruptly, and
// Shutdown drains in-flight events through one final flush.
type fsWatcher struct {
	hostRoot   string
	ignore     []string
	errHandler func(error)
	watcher    *fsnotify.Watcher

	mu          sync.Mutex
	watchedDirs map[string]struct{}

	started  bool
	done     chan struct{}
	exited   chan struct{}
	stopOnce sync.Once
}

// newFSWatcher constructs an fsnotify-backed watcher rooted at
// hostRoot and registers watches on every directory in the tree not
// matched by ignore. Returns ErrFSWatcherSetupFailed (wrapping the
// underlying cause) when fsnotify.NewWatcher fails or the initial walk
// can't even read hostRoot itself; per-subdir Add failures are routed
// to errHandler and skipped (one unreadable subdir does not fail
// construction).
func newFSWatcher(hostRoot string, ignore []string, errHandler func(error)) (*fsWatcher, error) {
	abs, err := filepath.Abs(hostRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve hostRoot %q: %v", ErrFSWatcherSetupFailed, hostRoot, err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFSWatcherSetupFailed, err)
	}
	fw := &fsWatcher{
		hostRoot:    abs,
		ignore:      mergeIgnore(ignore),
		errHandler:  errHandler,
		watcher:     w,
		watchedDirs: map[string]struct{}{},
		done:        make(chan struct{}),
		exited:      make(chan struct{}),
	}
	if err := fw.walkAndWatch(abs); err != nil {
		w.Close()
		return nil, fmt.Errorf("%w: initial walk of %q: %v", ErrFSWatcherSetupFailed, abs, err)
	}
	return fw, nil
}

// mergeIgnore prepends defaults so a caller-supplied empty slice still
// gets the always-on set; supplied patterns supplement the defaults.
func mergeIgnore(extra []string) []string {
	out := append([]string{}, defaultFSWatcherIgnore...)
	for _, p := range extra {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// walkAndWatch enumerates every directory under root and registers a
// watch on each one not matched by the ignore set. Errors from
// individual watcher.Add calls go through errHandler so a single
// permission-denied subdir does not abort the whole walk.
func (fw *fsWatcher) walkAndWatch(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fw.reportErr(fmt.Errorf("walk %q: %w", p, err))
			if p == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if fw.isIgnored(p) {
			return filepath.SkipDir
		}
		fw.mu.Lock()
		_, already := fw.watchedDirs[p]
		fw.mu.Unlock()
		if already {
			return nil
		}
		if err := fw.watcher.Add(p); err != nil {
			fw.reportErr(fmt.Errorf("watch add %q: %w", p, err))
			return nil
		}
		fw.mu.Lock()
		fw.watchedDirs[p] = struct{}{}
		fw.mu.Unlock()
		return nil
	})
}

// isIgnored returns true when p contains an ignored segment, treated
// as a path-component match relative to hostRoot.
func (fw *fsWatcher) isIgnored(p string) bool {
	rel, err := filepath.Rel(fw.hostRoot, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, seg := range parts {
		for _, pat := range fw.ignore {
			if seg == pat {
				return true
			}
		}
	}
	return false
}

// reportErr delivers an error to the configured handler when set;
// silently drops otherwise. Never blocks the watcher goroutine —
// handlers MUST themselves be non-blocking.
func (fw *fsWatcher) reportErr(err error) {
	if fw.errHandler == nil {
		return
	}
	fw.errHandler(err)
}

// start spawns the dispatch goroutine that forwards fsnotify events
// into provider.NotifyChangedEvents. Idempotent: a second call is a
// no-op. The goroutine exits when Close or Shutdown signals done OR
// when fsnotify's channels close on their own.
func (fw *fsWatcher) start(p *Provider) {
	fw.mu.Lock()
	if fw.started {
		fw.mu.Unlock()
		return
	}
	fw.started = true
	fw.mu.Unlock()
	go fw.run(p)
}

// run is the dispatch loop. Reads fsnotify events, maps them into
// PathChange + ChangeAction, batches them per outer NotifyChangedEvents
// call (the Applier's coalesce window further collapses burst events
// from editor saves). On done, drains buffered events non-blockingly
// before exiting so Shutdown's graceful path doesn't lose state.
func (fw *fsWatcher) run(p *Provider) {
	defer close(fw.exited)
	for {
		select {
		case event, ok := <-fw.watcher.Events:
			if !ok {
				return
			}
			fw.handleEvent(p, event)
		case err, ok := <-fw.watcher.Errors:
			if !ok {
				return
			}
			fw.reportErr(fmt.Errorf("fsnotify runtime: %w", err))
		case <-fw.done:
			fw.drainAndExit(p)
			return
		}
	}
}

// drainAndExit consumes any events already buffered in the fsnotify
// channel and forwards them, then returns. Called from run when done
// fires so Shutdown's caller sees a clean state before the goroutine
// exits.
func (fw *fsWatcher) drainAndExit(p *Provider) {
	for {
		select {
		case event, ok := <-fw.watcher.Events:
			if !ok {
				return
			}
			fw.handleEvent(p, event)
		default:
			return
		}
	}
}

// handleEvent maps one fsnotify.Event into a PathChange + forwards it
// to the Applier. Directory-create events extend the watch set;
// directory-remove events prune it. Path-traversal of the watch set
// uses the hostRoot-relative form so PathChange.Path matches what
// Provider.NotifyChangedEvents expects.
func (fw *fsWatcher) handleEvent(p *Provider, event fsnotify.Event) {
	if fw.isIgnored(event.Name) {
		return
	}
	rel, err := filepath.Rel(fw.hostRoot, event.Name)
	if err != nil {
		fw.reportErr(fmt.Errorf("rel path for %q: %w", event.Name, err))
		return
	}
	rel = filepath.ToSlash(rel)

	action, forward := mapFSNotifyOp(event.Op)
	if forward {
		change := PathChange{
			Path:      rel,
			Action:    action,
			Timestamp: time.Now(),
		}
		_ = p.NotifyChangedEvents(change)
	}

	if event.Op&fsnotify.Create != 0 {
		fw.extendWatchIfDir(event.Name)
	}
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		fw.pruneWatch(event.Name)
	}
}

// mapFSNotifyOp translates an fsnotify.Op bitmask into the
// ChangeAction that subscribers will see. Returns forward=false for
// no-op events (currently none — kept as the seam if e.g. Chmod-only
// events should be dropped later).
func mapFSNotifyOp(op fsnotify.Op) (ChangeAction, bool) {
	switch {
	case op&fsnotify.Create != 0:
		return ChangeActionCreated, true
	case op&(fsnotify.Remove|fsnotify.Rename) != 0:
		return ChangeActionDeleted, true
	case op&(fsnotify.Write|fsnotify.Chmod) != 0:
		return ChangeActionModified, true
	}
	return "", false
}

// extendWatchIfDir registers a watch on path when it points at a
// directory (so newly created subtrees become observable without a
// manual walk). Silently no-ops on stat errors — the Create event
// already fired into the Applier; missing the recursive expansion is
// a degradation, not a data-loss bug.
func (fw *fsWatcher) extendWatchIfDir(p string) {
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return
	}
	if fw.isIgnored(p) {
		return
	}
	if err := fw.walkAndWatch(p); err != nil {
		fw.reportErr(fmt.Errorf("recursive watch %q: %w", p, err))
	}
}

// pruneWatch removes path from the watch set on directory removal so
// fsnotify's per-watcher capacity isn't pinned by ghost entries.
// fsnotify auto-removes the kernel watch when the inode goes away;
// this mirrors our bookkeeping so re-creating the same path later
// re-walks cleanly.
func (fw *fsWatcher) pruneWatch(p string) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	for dir := range fw.watchedDirs {
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) {
			delete(fw.watchedDirs, dir)
		}
	}
}

// close stops the dispatch goroutine abruptly and releases the
// underlying fsnotify.Watcher. Idempotent. Pending events in the
// fsnotify channel are dropped.
func (fw *fsWatcher) close() error {
	if fw == nil {
		return nil
	}
	var err error
	fw.stopOnce.Do(func() {
		close(fw.done)
		err = fw.watcher.Close()
	})
	return err
}

// shutdown signals graceful drain: the dispatch goroutine consumes
// any events still buffered in the fsnotify channel, forwards them
// through the Applier, then exits. Waits for the goroutine to exit
// or for ctx to cancel, whichever fires first; on ctx cancel the
// watcher is still closed cleanly but ctx.Err() is returned so the
// caller knows the drain didn't complete.
func (fw *fsWatcher) shutdown(ctx context.Context) error {
	if fw == nil {
		return nil
	}
	var ferr error
	fw.stopOnce.Do(func() {
		close(fw.done)
	})
	select {
	case <-fw.exited:
	case <-ctx.Done():
		ferr = ctx.Err()
	}
	if cerr := fw.watcher.Close(); cerr != nil && ferr == nil {
		ferr = cerr
	}
	if errors.Is(ferr, context.Canceled) || errors.Is(ferr, context.DeadlineExceeded) {
		return ferr
	}
	return nil
}


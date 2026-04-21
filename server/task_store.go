// Package tasks is EXPERIMENTAL and subject to breaking changes.
//
// It implements the MCP Tasks protocol (spec 2025-11-25) as a reusable
// library on top of mcpkit. Servers register task-capable tools; the library
// handles protocol methods (tasks/get, tasks/result, tasks/list, tasks/cancel),
// middleware-based tools/call interception, and in-memory task state management.
//
// Stability: experimental. The Go API will change as the spec evolves and
// conformance tests are published.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// TaskStore is the interface for task state persistence. Implementations
// must be safe for concurrent use.
//
// All methods accept a sessionID parameter for session isolation.
// Empty sessionID means no session binding (backward compatible).
// When both the task and the caller have a sessionID, they must match.
type TaskStore interface {
	// Create persists a new task bound to the given session.
	Create(info core.TaskInfo, sessionID string) error

	// Get returns a task by ID, or false if not found or session mismatch.
	Get(taskID, sessionID string) (core.TaskInfo, bool)

	// Update atomically modifies a task via the provided function.
	// Returns an error if the task doesn't exist or session mismatch.
	Update(taskID, sessionID string, fn func(*core.TaskInfo)) error

	// SetResult stores the tool result for a completed task.
	SetResult(taskID, sessionID string, result core.ToolResult) error

	// GetResult returns the stored tool result, or false if not yet available.
	GetResult(taskID, sessionID string) (core.ToolResult, bool)

	// WaitForResult blocks until the task reaches a terminal state, then
	// returns the result. Respects context cancellation.
	WaitForResult(ctx context.Context, taskID, sessionID string) (core.ToolResult, core.TaskInfo, error)

	// List returns tasks for the given session with cursor-based pagination.
	List(cursor string, limit int, sessionID string) ([]core.TaskInfo, string)

	// WaitForUpdate blocks until the task's state changes, or the context
	// is cancelled. Used by the tasks/result long-poll loop.
	WaitForUpdate(ctx context.Context, taskID, sessionID string) error

	// Cancel transitions a non-terminal task to cancelled.
	Cancel(taskID, sessionID string) (core.TaskInfo, error)

	// Cleanup removes all tasks and stops any background timers.
	Cleanup()
}

// taskEntry holds all state for a single task in the in-memory store.
type taskEntry struct {
	info      core.TaskInfo
	result    json.RawMessage // stored tool result (nil until terminal)
	waiters   []chan struct{}  // channels to notify on status/result changes
	timer     *time.Timer     // TTL cleanup timer; nil if TTL is null/unlimited
	sessionID string          // session that created this task; empty = no binding
}

// sessionAllowed checks if the caller's session can access this task.
// Access is allowed if either side has no sessionID (backward compat).
func (e *taskEntry) sessionAllowed(callerSession string) bool {
	if callerSession == "" || e.sessionID == "" {
		return true
	}
	return callerSession == e.sessionID
}

// notify wakes all waiters for this task entry.
func (e *taskEntry) notify() {
	for _, ch := range e.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	e.waiters = nil
}

// InMemoryTaskStore is a TaskStore backed by an in-memory map with insertion-ordered
// keys for cursor pagination.
type InMemoryTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*taskEntry
	order []string // insertion-ordered task IDs for cursor pagination
}

// NewInMemoryStore creates a new in-memory task store.
func NewInMemoryStore() *InMemoryTaskStore {
	return &InMemoryTaskStore{
		tasks: make(map[string]*taskEntry),
	}
}

func (s *InMemoryTaskStore) Create(info core.TaskInfo, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[info.TaskID]; exists {
		return fmt.Errorf("task %q already exists", info.TaskID)
	}
	entry := &taskEntry{info: info, sessionID: sessionID}
	// Schedule TTL cleanup if TTL is set and positive.
	if info.TTL != nil && *info.TTL > 0 {
		taskID := info.TaskID
		entry.timer = time.AfterFunc(time.Duration(*info.TTL)*time.Millisecond, func() {
			s.deleteTask(taskID)
		})
	}
	s.tasks[info.TaskID] = entry
	s.order = append(s.order, info.TaskID)
	return nil
}

// getEntry returns the task entry if it exists and the caller's session
// is allowed. Must be called with at least s.mu.RLock held.
func (s *InMemoryTaskStore) getEntry(taskID, sessionID string) (*taskEntry, bool) {
	entry, ok := s.tasks[taskID]
	if !ok || !entry.sessionAllowed(sessionID) {
		return nil, false
	}
	return entry, true
}

func (s *InMemoryTaskStore) Get(taskID, sessionID string) (core.TaskInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok {
		return core.TaskInfo{}, false
	}
	return entry.info, true
}

func (s *InMemoryTaskStore) Update(taskID, sessionID string, fn func(*core.TaskInfo)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	fn(&entry.info)
	entry.notify()
	return nil
}

func (s *InMemoryTaskStore) SetResult(taskID, sessionID string, result core.ToolResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	entry.result = raw
	s.resetTimer(entry)
	entry.notify()
	return nil
}

func (s *InMemoryTaskStore) GetResult(taskID, sessionID string) (core.ToolResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok || entry.result == nil {
		return core.ToolResult{}, false
	}
	var result core.ToolResult
	json.Unmarshal(entry.result, &result)
	return result, true
}

func (s *InMemoryTaskStore) WaitForResult(ctx context.Context, taskID, sessionID string) (core.ToolResult, core.TaskInfo, error) {
	for {
		s.mu.Lock()
		entry, ok := s.getEntry(taskID, sessionID)
		if !ok {
			s.mu.Unlock()
			return core.ToolResult{}, core.TaskInfo{}, fmt.Errorf("task %q not found", taskID)
		}
		if entry.info.Status.IsTerminal() {
			var result core.ToolResult
			if entry.result != nil {
				json.Unmarshal(entry.result, &result)
			}
			info := entry.info
			s.mu.Unlock()
			return result, info, nil
		}

		ch := make(chan struct{}, 1)
		entry.waiters = append(entry.waiters, ch)
		s.mu.Unlock()

		select {
		case <-ch:
			continue
		case <-ctx.Done():
			return core.ToolResult{}, core.TaskInfo{}, ctx.Err()
		}
	}
}

func (s *InMemoryTaskStore) List(cursor string, limit int, sessionID string) ([]core.TaskInfo, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	start := 0
	if cursor != "" {
		for i, id := range s.order {
			if id == cursor {
				start = i + 1
				break
			}
		}
	}

	var tasks []core.TaskInfo
	var nextCursor string
	for i := start; i < len(s.order) && len(tasks) < limit; i++ {
		if entry, ok := s.tasks[s.order[i]]; ok {
			if entry.sessionAllowed(sessionID) {
				tasks = append(tasks, entry.info)
			}
		}
	}
	if start+limit < len(s.order) && len(tasks) == limit {
		nextCursor = tasks[len(tasks)-1].TaskID
	}

	return tasks, nextCursor
}

func (s *InMemoryTaskStore) WaitForUpdate(ctx context.Context, taskID, sessionID string) error {
	s.mu.Lock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("task %q not found", taskID)
	}

	ch := make(chan struct{}, 1)
	entry.waiters = append(entry.waiters, ch)
	s.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var errTaskTerminal = errors.New("task is already in a terminal state")

func (s *InMemoryTaskStore) Cancel(taskID, sessionID string) (core.TaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.getEntry(taskID, sessionID)
	if !ok {
		return core.TaskInfo{}, fmt.Errorf("task %q not found", taskID)
	}
	if entry.info.Status.IsTerminal() {
		return entry.info, errTaskTerminal
	}
	entry.info.Status = core.TaskCancelled
	entry.info.StatusMessage = "task was cancelled"
	if entry.result == nil {
		cancelResult := core.ToolResult{
			Content: []core.Content{{Type: "text", Text: "task was cancelled"}},
			IsError: true,
		}
		raw, _ := json.Marshal(cancelResult)
		entry.result = raw
	}
	s.resetTimer(entry)
	entry.notify()
	return entry.info, nil
}

// deleteTask removes a task from the store. Called by the TTL timer when
// it fires. Thread-safe — acquires its own lock.
func (s *InMemoryTaskStore) deleteTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.tasks[taskID]; ok {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		entry.notify() // wake any waiters so they see "not found"
		delete(s.tasks, taskID)
	}
}

// resetTimer stops the existing TTL timer (if any) and starts a new one
// with the task's TTL. Called when the task's lifetime should restart
// (e.g., result stored, status changed to terminal).
// Must be called with s.mu held.
func (s *InMemoryTaskStore) resetTimer(entry *taskEntry) {
	if entry.info.TTL == nil || *entry.info.TTL <= 0 {
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	taskID := entry.info.TaskID
	entry.timer = time.AfterFunc(time.Duration(*entry.info.TTL)*time.Millisecond, func() {
		s.deleteTask(taskID)
	})
}

// Cleanup removes all tasks and stops all TTL timers.
// Used for graceful shutdown and testing.
func (s *InMemoryTaskStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.tasks {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		entry.notify()
	}
	s.tasks = make(map[string]*taskEntry)
	s.order = nil
}

// Package tasks is EXPERIMENTAL and subject to breaking changes.
//
// It implements the MCP Tasks protocol (spec 2025-11-25) as a reusable
// library on top of mcpkit. Servers register task-capable tools; the library
// handles protocol methods (tasks/get, tasks/result, tasks/list, tasks/cancel),
// middleware-based tools/call interception, and in-memory task state management.
//
// Stability: experimental. The Go API will change as the spec evolves and
// conformance tests are published.
package tasks

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
type TaskStore interface {
	// Create persists a new task. Returns an error if the taskId already exists.
	Create(info core.TaskInfo) error

	// Get returns a task by ID, or false if not found.
	Get(taskID string) (core.TaskInfo, bool)

	// Update atomically modifies a task via the provided function.
	// Returns an error if the task doesn't exist.
	Update(taskID string, fn func(*core.TaskInfo)) error

	// SetResult stores the tool result for a completed task.
	SetResult(taskID string, result core.ToolResult) error

	// GetResult returns the stored tool result, or false if not yet available.
	GetResult(taskID string) (core.ToolResult, bool)

	// WaitForResult blocks until the task reaches a terminal state, then
	// returns the result. Returns an error if the task doesn't exist.
	// Respects context cancellation.
	WaitForResult(ctx context.Context, taskID string) (core.ToolResult, core.TaskInfo, error)

	// List returns tasks with cursor-based pagination. An empty cursor
	// starts from the beginning.
	List(cursor string, limit int) ([]core.TaskInfo, string)

	// WaitForUpdate blocks until the task's state changes (status or result),
	// or the context is cancelled. Used by the tasks/result long-poll loop.
	WaitForUpdate(ctx context.Context, taskID string) error

	// Cancel transitions a non-terminal task to cancelled. Returns an error
	// if the task is already terminal or doesn't exist.
	Cancel(taskID string) (core.TaskInfo, error)

	// Cleanup removes all tasks and stops any background timers.
	// Used for graceful shutdown and testing.
	Cleanup()
}

// taskEntry holds all state for a single task in the in-memory store.
type taskEntry struct {
	info    core.TaskInfo
	result  json.RawMessage // stored tool result (nil until terminal)
	waiters []chan struct{}  // channels to notify on status/result changes
	timer   *time.Timer     // TTL cleanup timer; nil if TTL is null/unlimited
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

func (s *InMemoryTaskStore) Create(info core.TaskInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[info.TaskID]; exists {
		return fmt.Errorf("task %q already exists", info.TaskID)
	}
	entry := &taskEntry{info: info}
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

func (s *InMemoryTaskStore) Get(taskID string) (core.TaskInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.tasks[taskID]
	if !ok {
		return core.TaskInfo{}, false
	}
	return entry.info, true
}

func (s *InMemoryTaskStore) Update(taskID string, fn func(*core.TaskInfo)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	fn(&entry.info)
	entry.notify()
	return nil
}

func (s *InMemoryTaskStore) SetResult(taskID string, result core.ToolResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	entry.result = raw
	// Reset TTL timer — task gets a fresh TTL window from now.
	s.resetTimer(entry)
	entry.notify()
	return nil
}

func (s *InMemoryTaskStore) GetResult(taskID string) (core.ToolResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.tasks[taskID]
	if !ok || entry.result == nil {
		return core.ToolResult{}, false
	}
	var result core.ToolResult
	json.Unmarshal(entry.result, &result)
	return result, true
}

// WaitForResult blocks until the task reaches a terminal state and a result
// is available, or the context is cancelled. Returns context.Canceled if
// the context is done before the task completes.
func (s *InMemoryTaskStore) WaitForResult(ctx context.Context, taskID string) (core.ToolResult, core.TaskInfo, error) {
	for {
		// Check current state under lock.
		s.mu.Lock()
		entry, ok := s.tasks[taskID]
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

		// Not terminal — register a waiter channel and wait.
		ch := make(chan struct{}, 1)
		entry.waiters = append(entry.waiters, ch)
		s.mu.Unlock()

		// Wait for either an update or context cancellation.
		select {
		case <-ch:
			// Task was updated — loop back to check state.
			continue
		case <-ctx.Done():
			return core.ToolResult{}, core.TaskInfo{}, ctx.Err()
		}
	}
}

func (s *InMemoryTaskStore) List(cursor string, limit int) ([]core.TaskInfo, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	// Find starting index from cursor (cursor is a task ID).
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
			tasks = append(tasks, entry.info)
		}
	}
	if start+limit < len(s.order) && len(tasks) == limit {
		nextCursor = tasks[len(tasks)-1].TaskID
	}

	return tasks, nextCursor
}

// WaitForUpdate blocks until the task's state changes or the context is cancelled.
// Returns nil on update, context error on cancellation, or an error if the task
// doesn't exist.
func (s *InMemoryTaskStore) WaitForUpdate(ctx context.Context, taskID string) error {
	s.mu.Lock()
	entry, ok := s.tasks[taskID]
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

func (s *InMemoryTaskStore) Cancel(taskID string) (core.TaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tasks[taskID]
	if !ok {
		return core.TaskInfo{}, fmt.Errorf("task %q not found", taskID)
	}
	if entry.info.Status.IsTerminal() {
		return entry.info, errTaskTerminal
	}
	entry.info.Status = core.TaskCancelled
	entry.info.StatusMessage = "task was cancelled"
	// Store a cancellation result so tasks/result can return it.
	if entry.result == nil {
		cancelResult := core.ToolResult{
			Content: []core.Content{{Type: "text", Text: "task was cancelled"}},
			IsError: true,
		}
		raw, _ := json.Marshal(cancelResult)
		entry.result = raw
	}
	// Reset TTL timer — cancelled task gets a fresh TTL window for cleanup.
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

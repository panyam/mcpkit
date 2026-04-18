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
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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

	// SetResult stores the tool result for a completed task and unblocks
	// any waiters on WaitForResult.
	SetResult(taskID string, result core.ToolResult) error

	// GetResult returns the stored tool result, or false if not yet available.
	GetResult(taskID string) (core.ToolResult, bool)

	// WaitForResult blocks until the task reaches a terminal state, then
	// returns the result. Returns an error if the task doesn't exist.
	WaitForResult(taskID string) (core.ToolResult, core.TaskInfo, error)

	// List returns tasks with cursor-based pagination. An empty cursor
	// starts from the beginning.
	List(cursor string, limit int) ([]core.TaskInfo, string)

	// Cancel transitions a non-terminal task to cancelled. Returns an error
	// if the task is already terminal or doesn't exist.
	Cancel(taskID string) (core.TaskInfo, error)
}

// InMemoryTaskStore is a TaskStore backed by an in-memory map with insertion-ordered
// keys for cursor pagination. Uses sync.Cond for WaitForResult blocking.
type InMemoryTaskStore struct {
	mu       sync.RWMutex
	cond     *sync.Cond
	tasks    map[string]*taskEntry
	order    []string // insertion-ordered task IDs for cursor pagination
	results  map[string]json.RawMessage
}

type taskEntry struct {
	info core.TaskInfo
}

// NewInMemoryStore creates a new in-memory task store.
func NewInMemoryStore() *InMemoryTaskStore {
	s := &InMemoryTaskStore{
		tasks:   make(map[string]*taskEntry),
		results: make(map[string]json.RawMessage),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *InMemoryTaskStore) Create(info core.TaskInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[info.TaskID]; exists {
		return fmt.Errorf("task %q already exists", info.TaskID)
	}
	s.tasks[info.TaskID] = &taskEntry{info: info}
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
	s.cond.Broadcast()
	return nil
}

func (s *InMemoryTaskStore) SetResult(taskID string, result core.ToolResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[taskID]; !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	s.results[taskID] = raw
	s.cond.Broadcast()
	return nil
}

func (s *InMemoryTaskStore) GetResult(taskID string) (core.ToolResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, ok := s.results[taskID]
	if !ok {
		return core.ToolResult{}, false
	}
	var result core.ToolResult
	json.Unmarshal(raw, &result)
	return result, true
}

// WaitForResult blocks until the task reaches a terminal state and a result
// is available. The caller must ensure the task exists before calling.
func (s *InMemoryTaskStore) WaitForResult(taskID string) (core.ToolResult, core.TaskInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		entry, ok := s.tasks[taskID]
		if !ok {
			return core.ToolResult{}, core.TaskInfo{}, fmt.Errorf("task %q not found", taskID)
		}
		if entry.info.Status.IsTerminal() {
			raw, hasResult := s.results[taskID]
			if !hasResult {
				// Cancelled or failed without a result.
				return core.ToolResult{}, entry.info, nil
			}
			var result core.ToolResult
			json.Unmarshal(raw, &result)
			return result, entry.info, nil
		}
		s.cond.Wait()
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
	s.cond.Broadcast()
	return entry.info, nil
}

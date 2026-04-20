package tasks

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

func newTestInfo(id string, status core.TaskStatus) core.TaskInfo {
	now := time.Now().UTC().Format(time.RFC3339)
	return core.TaskInfo{
		TaskID:        id,
		Status:        status,
		CreatedAt:     now,
		LastUpdatedAt: now,
		TTL:           core.IntPtr(300_000),
		PollInterval:  1000,
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	s := NewInMemoryStore()

	info := newTestInfo("t1", core.TaskWorking)
	if err := s.Create(info); err != nil {
		t.Fatal(err)
	}

	got, ok := s.Get("t1")
	if !ok {
		t.Fatal("expected to find task t1")
	}
	if got.TaskID != "t1" || got.Status != core.TaskWorking {
		t.Errorf("got %+v, want id=t1 status=working", got)
	}
}

func TestStoreCreateDuplicate(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	if err := s.Create(info); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(info); err == nil {
		t.Error("expected error on duplicate create")
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestStoreUpdate(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	err := s.Update("t1", func(info *core.TaskInfo) {
		info.Status = core.TaskCompleted
		info.StatusMessage = "done"
	})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get("t1")
	if got.Status != core.TaskCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.StatusMessage != "done" {
		t.Errorf("statusMessage = %q, want done", got.StatusMessage)
	}
}

func TestStoreUpdateNotFound(t *testing.T) {
	s := NewInMemoryStore()
	err := s.Update("nonexistent", func(*core.TaskInfo) {})
	if err == nil {
		t.Error("expected error on update of nonexistent task")
	}
}

func TestStoreSetAndGetResult(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	result := core.TextResult("hello world")
	if err := s.SetResult("t1", result); err != nil {
		t.Fatal(err)
	}

	got, ok := s.GetResult("t1")
	if !ok {
		t.Fatal("expected result to be present")
	}
	if len(got.Content) == 0 {
		t.Fatal("expected non-empty content")
	}
	if got.Content[0].Text != "hello world" {
		t.Errorf("text = %q, want 'hello world'", got.Content[0].Text)
	}
}

func TestStoreGetResultNotReady(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	_, ok := s.GetResult("t1")
	if ok {
		t.Error("expected no result yet")
	}
}

func TestStoreWaitForResult(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	done := make(chan struct{})
	var gotResult core.ToolResult
	var gotInfo core.TaskInfo
	var gotErr error

	go func() {
		gotResult, gotInfo, gotErr = s.WaitForResult(context.Background(), "t1")
		close(done)
	}()

	// Give the goroutine time to block.
	time.Sleep(50 * time.Millisecond)

	// Store result first, then transition to terminal (Update broadcasts
	// to waiters, so the result must be available before wake-up).
	s.SetResult("t1", core.TextResult("waited"))
	s.Update("t1", func(info *core.TaskInfo) {
		info.Status = core.TaskCompleted
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForResult did not return in time")
	}

	if gotErr != nil {
		t.Fatal(gotErr)
	}
	if gotInfo.Status != core.TaskCompleted {
		t.Errorf("status = %q, want completed", gotInfo.Status)
	}
	if len(gotResult.Content) == 0 || gotResult.Content[0].Text != "waited" {
		t.Errorf("unexpected result: %+v", gotResult)
	}
}

func TestStoreWaitForResultNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, _, err := s.WaitForResult(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestStoreCancel(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	info, err := s.Cancel("t1")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", info.Status)
	}

	// Re-cancel should fail (already terminal).
	_, err = s.Cancel("t1")
	if err == nil {
		t.Error("expected error when cancelling already-terminal task")
	}
}

func TestStoreCancelNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, err := s.Cancel("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestStoreCancelUnblocksWaiter(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	done := make(chan struct{})
	var gotInfo core.TaskInfo

	go func() {
		_, gotInfo, _ = s.WaitForResult(context.Background(), "t1")
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	s.Cancel("t1")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForResult did not unblock after cancel")
	}

	if gotInfo.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", gotInfo.Status)
	}
}

func TestStoreWaitForResultContextCancelled(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, _, err := s.WaitForResult(ctx, "t1")
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForResult did not return after context cancellation")
	}
}

func TestStoreWaitForUpdateWakesOnUpdate(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForUpdate(context.Background(), "t1")
	}()

	time.Sleep(50 * time.Millisecond)
	// Any Update should wake the waiter — not just terminal.
	s.Update("t1", func(info *core.TaskInfo) {
		info.StatusMessage = "progress 50%"
	})

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitForUpdate returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForUpdate did not wake on Update")
	}
}

func TestStoreWaitForUpdateWakesOnSetResult(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	done := make(chan error, 1)
	go func() {
		done <- s.WaitForUpdate(context.Background(), "t1")
	}()

	time.Sleep(50 * time.Millisecond)
	s.SetResult("t1", core.TextResult("partial"))

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitForUpdate returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForUpdate did not wake on SetResult")
	}
}

func TestStoreWaitForUpdateContextCancelled(t *testing.T) {
	s := NewInMemoryStore()
	s.Create(newTestInfo("t1", core.TaskWorking))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.WaitForUpdate(ctx, "t1")
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForUpdate did not return after context cancellation")
	}
}

func TestStoreWaitForUpdateNotFound(t *testing.T) {
	s := NewInMemoryStore()
	err := s.WaitForUpdate(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestStoreListPagination(t *testing.T) {
	s := NewInMemoryStore()
	for i := 0; i < 5; i++ {
		s.Create(newTestInfo("t"+string(rune('0'+i)), core.TaskWorking))
	}

	// First page: limit 3.
	tasks, cursor := s.List("", 3)
	if len(tasks) != 3 {
		t.Fatalf("page 1: got %d tasks, want 3", len(tasks))
	}
	if cursor == "" {
		t.Fatal("expected a next cursor for first page")
	}

	// Second page from cursor.
	tasks2, cursor2 := s.List(cursor, 3)
	if len(tasks2) != 2 {
		t.Fatalf("page 2: got %d tasks, want 2", len(tasks2))
	}
	if cursor2 != "" {
		t.Errorf("expected empty cursor on last page, got %q", cursor2)
	}
}

func TestStoreListEmpty(t *testing.T) {
	s := NewInMemoryStore()
	tasks, cursor := s.List("", 10)
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
	if cursor != "" {
		t.Errorf("expected empty cursor, got %q", cursor)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewInMemoryStore()
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "t" + time.Now().Format("150405.000000000") + "-" + string(rune('A'+i%26))
			s.Create(newTestInfo(id, core.TaskWorking))
			s.Get(id)
			s.Update(id, func(info *core.TaskInfo) {
				info.Status = core.TaskCompleted
			})
			s.SetResult(id, core.TextResult("done"))
			s.GetResult(id)
		}(i)
	}
	wg.Wait()

	tasks, _ := s.List("", 100)
	if len(tasks) != n {
		t.Errorf("got %d tasks, want %d", len(tasks), n)
	}
}

// --- TTL enforcement tests (Phase 2) ---

// TestStoreTTLExpiry verifies that a task with a short TTL is automatically
// removed from the store after the TTL elapses. This is the core TTL
// enforcement behavior — exercise 9 in the tasks README.
func TestStoreTTLExpiry(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	info.TTL = core.IntPtr(100) // 100ms TTL
	s.Create(info)

	// Task should be accessible immediately.
	_, ok := s.Get("t1")
	if !ok {
		t.Fatal("task should exist immediately after creation")
	}

	// Wait for TTL to expire.
	time.Sleep(150 * time.Millisecond)

	// Task should be gone.
	_, ok = s.Get("t1")
	if ok {
		t.Error("task should have been removed after TTL expired")
	}
}

// TestStoreTTLResetOnResult verifies that storing a result resets the TTL
// timer — the task gets a fresh TTL window from the time the result is
// stored, not from creation. This matches the TS SDK behavior where
// completed tasks remain queryable for a TTL period after completion.
func TestStoreTTLResetOnResult(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	info.TTL = core.IntPtr(200) // 200ms TTL
	s.Create(info)

	// Wait 100ms (half the TTL), then store result.
	time.Sleep(100 * time.Millisecond)
	s.SetResult("t1", core.TextResult("done"))
	s.Update("t1", func(i *core.TaskInfo) { i.Status = core.TaskCompleted })

	// Wait another 150ms — past the original TTL but within the reset window.
	time.Sleep(150 * time.Millisecond)

	// Task should still be accessible (timer was reset on SetResult).
	_, ok := s.Get("t1")
	if !ok {
		t.Error("task should still exist — TTL should have reset when result was stored")
	}

	// Wait for the full reset TTL to expire.
	time.Sleep(100 * time.Millisecond)

	_, ok = s.Get("t1")
	if ok {
		t.Error("task should have been removed after reset TTL expired")
	}
}

// TestStoreTTLResetOnCancel verifies that cancelling a task resets the TTL
// timer so the cancelled task remains queryable for the TTL period.
func TestStoreTTLResetOnCancel(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	info.TTL = core.IntPtr(200) // 200ms TTL
	s.Create(info)

	// Wait 100ms, then cancel.
	time.Sleep(100 * time.Millisecond)
	s.Cancel("t1")

	// Wait 150ms — past original TTL but within reset window.
	time.Sleep(150 * time.Millisecond)

	_, ok := s.Get("t1")
	if !ok {
		t.Error("cancelled task should still exist within reset TTL window")
	}
}

// TestStoreTTLNullNoExpiry verifies that a task with TTL=nil (null per spec,
// meaning unlimited lifetime) is never automatically removed.
func TestStoreTTLNullNoExpiry(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	info.TTL = nil // null = unlimited
	s.Create(info)

	time.Sleep(100 * time.Millisecond)

	_, ok := s.Get("t1")
	if !ok {
		t.Error("task with nil TTL should never expire")
	}
}

// TestStoreCleanup verifies that Cleanup() removes all tasks and stops
// all TTL timers. Used for graceful shutdown and testing.
func TestStoreCleanup(t *testing.T) {
	s := NewInMemoryStore()
	for i := 0; i < 5; i++ {
		info := newTestInfo("t"+string(rune('0'+i)), core.TaskWorking)
		info.TTL = core.IntPtr(60000) // long TTL — won't expire during test
		s.Create(info)
	}

	s.Cleanup()

	for i := 0; i < 5; i++ {
		_, ok := s.Get("t" + string(rune('0'+i)))
		if ok {
			t.Errorf("task t%d should have been removed by Cleanup", i)
		}
	}

	tasks, _ := s.List("", 100)
	if len(tasks) != 0 {
		t.Errorf("List should return empty after Cleanup, got %d", len(tasks))
	}
}

// TestStoreCleanupStopsTimers verifies that Cleanup() stops pending TTL
// timers so they don't fire after cleanup (which would panic or corrupt
// state on a reused store).
func TestStoreCleanupStopsTimers(t *testing.T) {
	s := NewInMemoryStore()
	info := newTestInfo("t1", core.TaskWorking)
	info.TTL = core.IntPtr(50) // very short TTL
	s.Create(info)

	// Cleanup before the timer fires.
	s.Cleanup()

	// Wait past the original TTL — timer should have been stopped.
	time.Sleep(100 * time.Millisecond)

	// No panic, no corruption. The store is empty and stable.
	tasks, _ := s.List("", 100)
	if len(tasks) != 0 {
		t.Errorf("store should be empty after Cleanup, got %d", len(tasks))
	}
}

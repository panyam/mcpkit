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

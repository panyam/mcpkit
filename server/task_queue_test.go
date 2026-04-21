package server

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestQueueEnqueueDequeue(t *testing.T) {
	q := NewInMemoryMessageQueue()

	msg := QueuedMessage{Type: QueuedMessageRequest, Timestamp: 1000, Message: json.RawMessage(`{"id":1}`)}
	if err := q.Enqueue("t1", msg, 0); err != nil {
		t.Fatal(err)
	}

	got, ok := q.Dequeue("t1")
	if !ok {
		t.Fatal("expected message")
	}
	if got.Type != QueuedMessageRequest {
		t.Errorf("type = %q, want request", got.Type)
	}

	// Second dequeue should be empty.
	_, ok = q.Dequeue("t1")
	if ok {
		t.Error("expected empty queue")
	}
}

func TestQueueFIFO(t *testing.T) {
	q := NewInMemoryMessageQueue()

	for i := 0; i < 3; i++ {
		q.Enqueue("t1", QueuedMessage{
			Type:      QueuedMessageNotification,
			Timestamp: int64(i),
			Message:   json.RawMessage(`{}`),
		}, 0)
	}

	for i := 0; i < 3; i++ {
		got, ok := q.Dequeue("t1")
		if !ok {
			t.Fatalf("expected message at index %d", i)
		}
		if got.Timestamp != int64(i) {
			t.Errorf("msg[%d].Timestamp = %d, want %d", i, got.Timestamp, i)
		}
	}
}

func TestQueueDequeueAll(t *testing.T) {
	q := NewInMemoryMessageQueue()
	q.Enqueue("t1", QueuedMessage{Type: QueuedMessageRequest, Message: json.RawMessage(`{}`)}, 0)
	q.Enqueue("t1", QueuedMessage{Type: QueuedMessageResponse, Message: json.RawMessage(`{}`)}, 0)

	msgs := q.DequeueAll("t1")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}

	// Queue should be empty now.
	_, ok := q.Dequeue("t1")
	if ok {
		t.Error("expected empty queue after DequeueAll")
	}
}

func TestQueueOverflow(t *testing.T) {
	q := NewInMemoryMessageQueue()
	q.Enqueue("t1", QueuedMessage{Message: json.RawMessage(`{}`)}, 2)
	q.Enqueue("t1", QueuedMessage{Message: json.RawMessage(`{}`)}, 2)

	err := q.Enqueue("t1", QueuedMessage{Message: json.RawMessage(`{}`)}, 2)
	if err == nil {
		t.Error("expected overflow error")
	}
}

func TestQueueWaitForMessage(t *testing.T) {
	q := NewInMemoryMessageQueue()
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ok := q.WaitForMessage("t1", done)
		if !ok {
			t.Error("WaitForMessage should return true when message arrives")
		}
	}()

	// Give the goroutine time to block.
	time.Sleep(50 * time.Millisecond)

	// Enqueue should wake the waiter.
	q.Enqueue("t1", QueuedMessage{Type: QueuedMessageRequest, Message: json.RawMessage(`{}`)}, 0)
	wg.Wait()
}

func TestQueueWaitForMessageCancelled(t *testing.T) {
	q := NewInMemoryMessageQueue()
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ok := q.WaitForMessage("t1", done)
		if ok {
			t.Error("WaitForMessage should return false when done is closed")
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(done)
	wg.Wait()
}

func TestQueueWaitForMessageAlreadyAvailable(t *testing.T) {
	q := NewInMemoryMessageQueue()
	q.Enqueue("t1", QueuedMessage{Message: json.RawMessage(`{}`)}, 0)

	// Should return immediately since message is already queued.
	done := make(chan struct{})
	ok := q.WaitForMessage("t1", done)
	if !ok {
		t.Error("WaitForMessage should return true when messages already available")
	}
}

func TestQueueCleanup(t *testing.T) {
	q := NewInMemoryMessageQueue()
	q.Enqueue("t1", QueuedMessage{Message: json.RawMessage(`{}`)}, 0)
	q.Enqueue("t2", QueuedMessage{Message: json.RawMessage(`{}`)}, 0)

	q.Cleanup()

	_, ok := q.Dequeue("t1")
	if ok {
		t.Error("expected empty queue after Cleanup")
	}
}

func TestQueueIsolation(t *testing.T) {
	q := NewInMemoryMessageQueue()
	q.Enqueue("t1", QueuedMessage{Type: QueuedMessageRequest, Message: json.RawMessage(`{}`)}, 0)
	q.Enqueue("t2", QueuedMessage{Type: QueuedMessageResponse, Message: json.RawMessage(`{}`)}, 0)

	got, ok := q.Dequeue("t1")
	if !ok || got.Type != QueuedMessageRequest {
		t.Error("t1 should get its own message")
	}

	got, ok = q.Dequeue("t2")
	if !ok || got.Type != QueuedMessageResponse {
		t.Error("t2 should get its own message")
	}
}

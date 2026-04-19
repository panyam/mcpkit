package tasks

import (
	"encoding/json"
	"errors"
	"sync"
)

// QueuedMessageType identifies the kind of message in the queue.
type QueuedMessageType string

const (
	QueuedMessageRequest      QueuedMessageType = "request"
	QueuedMessageNotification QueuedMessageType = "notification"
	QueuedMessageResponse     QueuedMessageType = "response"
	QueuedMessageError        QueuedMessageType = "error"
)

// QueuedMessage is a message stored in a task's side-channel queue.
// During the tasks/result long-poll, the server dequeues these and
// delivers them to the client (e.g., elicitation or sampling requests).
type QueuedMessage struct {
	Type      QueuedMessageType `json:"type"`
	Timestamp int64             `json:"timestamp"` // Unix milliseconds
	Message   json.RawMessage   `json:"message"`   // JSON-RPC request, notification, response, or error
}

// TaskMessageQueue is a per-task FIFO message queue for side-channel
// communication during async tool execution. When a background task
// needs to elicit or sample, it enqueues a request here. The tasks/result
// long-poll dequeues and delivers these messages to the client.
//
// Implementations must be safe for concurrent use.
type TaskMessageQueue interface {
	// Enqueue adds a message to the end of the queue for a task.
	// maxSize of 0 means unbounded.
	Enqueue(taskID string, msg QueuedMessage, maxSize int) error

	// Dequeue removes and returns the first message, or false if empty.
	Dequeue(taskID string) (QueuedMessage, bool)

	// DequeueAll removes and returns all messages for a task.
	DequeueAll(taskID string) []QueuedMessage

	// WaitForMessage blocks until a message is available for the task,
	// or the done channel is closed. Returns false if done was closed.
	WaitForMessage(taskID string, done <-chan struct{}) bool

	// Cleanup removes all queues (for shutdown/testing).
	Cleanup()
}

// InMemoryMessageQueue is an in-memory TaskMessageQueue implementation.
type InMemoryMessageQueue struct {
	mu      sync.Mutex
	queues  map[string][]QueuedMessage
	waiters map[string][]chan struct{}
}

// NewInMemoryMessageQueue creates a new in-memory message queue.
func NewInMemoryMessageQueue() *InMemoryMessageQueue {
	return &InMemoryMessageQueue{
		queues:  make(map[string][]QueuedMessage),
		waiters: make(map[string][]chan struct{}),
	}
}

func (q *InMemoryMessageQueue) Enqueue(taskID string, msg QueuedMessage, maxSize int) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	queue := q.queues[taskID]
	if maxSize > 0 && len(queue) >= maxSize {
		return errQueueOverflow
	}
	q.queues[taskID] = append(queue, msg)

	// Wake all waiters for this task.
	for _, ch := range q.waiters[taskID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	q.waiters[taskID] = nil

	return nil
}

func (q *InMemoryMessageQueue) Dequeue(taskID string) (QueuedMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	queue := q.queues[taskID]
	if len(queue) == 0 {
		return QueuedMessage{}, false
	}
	msg := queue[0]
	q.queues[taskID] = queue[1:]
	return msg, true
}

func (q *InMemoryMessageQueue) DequeueAll(taskID string) []QueuedMessage {
	q.mu.Lock()
	defer q.mu.Unlock()

	msgs := q.queues[taskID]
	delete(q.queues, taskID)
	return msgs
}

func (q *InMemoryMessageQueue) WaitForMessage(taskID string, done <-chan struct{}) bool {
	q.mu.Lock()

	// Check if messages already available.
	if len(q.queues[taskID]) > 0 {
		q.mu.Unlock()
		return true
	}

	// Register a waiter channel.
	ch := make(chan struct{}, 1)
	q.waiters[taskID] = append(q.waiters[taskID], ch)
	q.mu.Unlock()

	// Wait for either a message or cancellation.
	select {
	case <-ch:
		return true
	case <-done:
		return false
	}
}

func (q *InMemoryMessageQueue) Cleanup() {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Wake all waiters so they don't hang.
	for _, waiters := range q.waiters {
		for _, ch := range waiters {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	q.queues = make(map[string][]QueuedMessage)
	q.waiters = make(map[string][]chan struct{})
}

var errQueueOverflow = errors.New("task message queue overflow")

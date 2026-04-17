// Package main implements a Telegram MCP Events reference server demonstrating
// push, poll, and webhook delivery modes using mcpkit.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Message represents a Telegram message stored in the in-memory buffer.
type Message struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// MessageStore is a thread-safe in-memory message buffer with cursor-based retrieval.
// Message IDs are monotonically increasing, serving as cursors for poll consumers.
type MessageStore struct {
	mu       sync.RWMutex
	messages []Message
	nextID   int64
	maxSize  int

	// OnMessage is called after a message is added. Used to trigger fan-out
	// (broadcast, resource notify, webhook delivery) without coupling the
	// store to MCP server internals.
	OnMessage func(Message)
}

// NewMessageStore creates a store with the given maximum buffer size.
func NewMessageStore(maxSize int) *MessageStore {
	return &MessageStore{
		maxSize: maxSize,
	}
}

// Add inserts a message, assigns it a monotonic ID, and calls OnMessage if set.
// Returns the assigned ID.
func (s *MessageStore) Add(chatID int64, sender, text string, ts time.Time) int64 {
	s.mu.Lock()
	s.nextID++
	msg := Message{
		ID:        s.nextID,
		ChatID:    chatID,
		Sender:    sender,
		Text:      text,
		Timestamp: ts,
	}
	s.messages = append(s.messages, msg)
	if len(s.messages) > s.maxSize {
		s.messages = s.messages[len(s.messages)-s.maxSize:]
	}
	cb := s.OnMessage
	s.mu.Unlock()

	if cb != nil {
		cb(msg)
	}
	return msg.ID
}

// PollResult holds the result of a cursor-based poll.
type PollResult struct {
	Messages   []Message
	NextCursor int64
	CursorGap  bool // true if events were lost due to ring buffer wrap
}

// GetSince returns messages with ID > cursor, up to limit. If the cursor
// refers to an evicted message (ring buffer wrapped), CursorGap is true —
// the client should treat this as a signal that events were silently lost.
func (s *MessageStore) GetSince(cursor int64, limit int) PollResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	// Detect cursor gap: cursor is non-zero and older than the oldest buffered message.
	var cursorGap bool
	if cursor > 0 && len(s.messages) > 0 && cursor < s.messages[0].ID-1 {
		cursorGap = true
	}

	var result []Message
	for _, m := range s.messages {
		if m.ID > cursor {
			result = append(result, m)
			if len(result) >= limit {
				break
			}
		}
	}

	nextCursor := cursor
	if len(result) > 0 {
		nextCursor = result[len(result)-1].ID
	}
	return PollResult{Messages: result, NextCursor: nextCursor, CursorGap: cursorGap}
}

// GetByID returns the message with the given ID, or nil if not found.
func (s *MessageStore) GetByID(id int64) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.messages {
		if s.messages[i].ID == id {
			msg := s.messages[i]
			return &msg
		}
	}
	return nil
}

// Recent returns the last n messages (most recent last).
func (s *MessageStore) Recent(n int) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || n > len(s.messages) {
		n = len(s.messages)
	}
	start := len(s.messages) - n
	result := make([]Message, n)
	copy(result, s.messages[start:])
	return result
}

// Len returns the number of messages in the store.
func (s *MessageStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// handleTelegramWebhook processes a Telegram Bot API webhook POST, extracts
// the text message, and adds it to the store. Returns the stored message or nil.
func handleTelegramWebhook(store *MessageStore, r *http.Request) *Message {
	var update tgbotapi.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("[telegram] failed to decode webhook: %v", err)
		return nil
	}

	msg := update.Message
	if msg == nil || msg.Text == "" {
		return nil
	}

	sender := "unknown"
	if msg.From != nil {
		if msg.From.UserName != "" {
			sender = msg.From.UserName
		} else {
			sender = msg.From.FirstName
		}
	}

	ts := time.Unix(int64(msg.Date), 0)
	id := store.Add(msg.Chat.ID, sender, msg.Text, ts)

	stored := store.GetByID(id)
	return stored
}

// Package main implements a Discord MCP Events reference server demonstrating
// push, poll, and webhook delivery modes using mcpkit.
package main

import (
	"log"
	"sync"
	"time"
)

// Message represents a Discord message stored in the in-memory buffer.
type Message struct {
	ID        int64     `json:"id"`
	GuildID   string    `json:"guild_id"`
	ChannelID string    `json:"channel_id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// MessageStore is a thread-safe in-memory message buffer with cursor-based retrieval.
type MessageStore struct {
	mu       sync.RWMutex
	messages []Message
	nextID   int64
	maxSize  int

	// OnMessage is called after a message is added — used to trigger fan-out.
	OnMessage func(Message)
}

func NewMessageStore(maxSize int) *MessageStore {
	return &MessageStore{maxSize: maxSize}
}

// Add inserts a message, assigns a monotonic ID, and calls OnMessage if set.
func (s *MessageStore) Add(guildID, channelID, sender, text string, ts time.Time) int64 {
	s.mu.Lock()
	s.nextID++
	msg := Message{
		ID:        s.nextID,
		GuildID:   guildID,
		ChannelID: channelID,
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
	log.Printf("[discord] id=%d guild=%s channel=%s sender=%s text=%q", msg.ID, guildID, channelID, sender, text)
	return msg.ID
}

// PollResult holds the result of a cursor-based poll.
type PollResult struct {
	Messages   []Message
	NextCursor int64
	CursorGap  bool
}

// GetSince returns messages with ID > cursor, up to limit.
func (s *MessageStore) GetSince(cursor int64, limit int) PollResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

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

// Recent returns the last n messages.
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

func (s *MessageStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

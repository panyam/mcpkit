package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"context"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// MethodDelivery implements EventDelivery using custom JSON-RPC method handlers
// (events/list, events/poll, events/subscribe, events/unsubscribe). This matches
// Peter Alexander's design sketch where event operations are protocol-level,
// not tool calls.
//
// Uses server.WithMethodHandler (issue #266) for JSON-RPC methods and
// server.WithHTTPHandler for the send_message tool (kept as a tool since it's
// an action, not an event operation).
type MethodDelivery struct{}

// Register hooks up protocol-level event methods on the MCP server.
func (d *MethodDelivery) Register(srv *server.Server, store *MessageStore, webhooks *WebhookRegistry) {
	// events/list — returns the catalog of available event types.
	srv.HandleMethod("events/list", func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
		result := map[string]any{
			"events": []map[string]any{
				{
					"name":        "telegram.message",
					"description": "Fires when a message is received by the Telegram bot",
					"delivery":    []string{"push", "poll", "webhook"},
					"payloadSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"chat_id":    map[string]string{"type": "string"},
							"message_id": map[string]string{"type": "string"},
							"user":       map[string]string{"type": "string"},
							"text":       map[string]string{"type": "string"},
							"ts":         map[string]string{"type": "string", "format": "date-time"},
						},
					},
				},
			},
		}
		return core.NewResponse(id, result)
	})

	// events/poll — cursor-based poll matching Clare's schema.
	// Request: { subscriptions: [{id, name, cursor}], maxEvents? }
	// Response: { results: [{id, events, cursor, hasMore, nextPollSeconds}] }
	srv.HandleMethod("events/poll", func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			MaxEvents     int `json:"maxEvents,omitempty"`
			Subscriptions []struct {
				ID     string  `json:"id"`
				Name   string  `json:"name"`
				Cursor *string `json:"cursor"`
			} `json:"subscriptions"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.MaxEvents <= 0 {
			req.MaxEvents = 50
		}

		type pollResult struct {
			ID              string          `json:"id"`
			Events          []TelegramEvent `json:"events,omitempty"`
			Cursor          string          `json:"cursor"`
			HasMore         bool            `json:"hasMore"`
			NextPollSeconds int             `json:"nextPollSeconds,omitempty"`
			Error           *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}

		results := make([]pollResult, 0, len(req.Subscriptions))
		for _, sub := range req.Subscriptions {
			if sub.Name != "telegram.message" {
				results = append(results, pollResult{
					ID: sub.ID,
					Error: &struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					}{Code: -32001, Message: "EventNotFound"},
				})
				continue
			}

			var cursor int64
			if sub.Cursor != nil {
				cursor, _ = strconv.ParseInt(*sub.Cursor, 10, 64)
			}

			// Fetch one extra to detect hasMore
			msgs, nextCursor := store.GetSince(cursor, req.MaxEvents+1)
			hasMore := len(msgs) > req.MaxEvents
			if hasMore {
				msgs = msgs[:req.MaxEvents]
				nextCursor = msgs[len(msgs)-1].ID
			}
			events := make([]TelegramEvent, 0, len(msgs))
			for _, m := range msgs {
				events = append(events, messageToEvent(m))
			}

			results = append(results, pollResult{
				ID:              sub.ID,
				Events:          events,
				Cursor:          strconv.FormatInt(nextCursor, 10),
				HasMore:         hasMore,
				NextPollSeconds: 5,
			})
		}

		return core.NewResponse(id, map[string]any{"results": results})
	})

	// events/subscribe — webhook registration matching Clare's schema.
	// Request: { id, name, delivery: {mode: "webhook", url, secret?}, cursor? }
	// Response: { id, secret, cursor, refreshBefore }
	srv.HandleMethod("events/subscribe", func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			Delivery struct {
				Mode   string `json:"mode"`
				URL    string `json:"url"`
				Secret string `json:"secret,omitempty"`
			} `json:"delivery"`
			Cursor *string `json:"cursor"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.Name != "telegram.message" {
			return core.NewErrorResponse(id, -32001, "EventNotFound")
		}
		if req.Delivery.Mode != "webhook" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "only webhook delivery mode is supported")
		}
		if req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url is required")
		}
		// Spec: MUST validate callback URLs at subscribe time.
		if err := ValidateWebhookURL(req.Delivery.URL); err != nil {
			return core.NewErrorResponse(id, -32005, err.Error())
		}

		secret := req.Delivery.Secret
		if secret == "" {
			// TODO: use crypto/rand for production-grade entropy
			secret = fmt.Sprintf("whsec_%d", store.Len())
		}

		expiresAt := webhooks.Register(req.ID, req.Delivery.URL, secret)

		cursorStr := "0"
		if req.Cursor != nil {
			cursorStr = *req.Cursor
		}

		return core.NewResponse(id, map[string]any{
			"id":            req.ID,
			"secret":        secret,
			"cursor":        cursorStr,
			"refreshBefore": expiresAt.Format(time.RFC3339),
		})
	})

	// events/unsubscribe — remove a webhook subscription.
	srv.HandleMethod("events/unsubscribe", func(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			ID       string `json:"id"`
			Delivery *struct {
				URL string `json:"url"`
			} `json:"delivery,omitempty"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.Delivery == nil || req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url required")
		}
		webhooks.Unregister(req.Delivery.URL, req.ID)
		return core.NewResponse(id, map[string]any{})
	})
}

// messageToEvent converts a Message to a TelegramEvent envelope.
func messageToEvent(m Message) TelegramEvent {
	ts := m.Timestamp.Format("2006-01-02T15:04:05Z07:00")
	return TelegramEvent{
		EventID:   fmt.Sprintf("evt_%d", m.ID),
		Name:      "telegram.message",
		Timestamp: ts,
		Data: TelegramEventData{
			ChatID:    strconv.FormatInt(m.ChatID, 10),
			MessageID: strconv.FormatInt(m.ID, 10),
			User:      m.Sender,
			Text:      m.Text,
			Timestamp: ts,
		},
		Cursor: strconv.FormatInt(m.ID, 10),
	}
}

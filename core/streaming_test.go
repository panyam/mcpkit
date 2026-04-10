package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitContentSendsNotification verifies that EmitContent calls the
// session's notify function with the correct method name and ContentChunk
// payload when called within a session context.
func TestEmitContentSendsNotification(t *testing.T) {
	var calledMethod string
	var calledParams any

	notify := NotifyFunc(func(method string, params any) {
		calledMethod = method
		calledParams = params
	})

	ctx := ContextWithSession(context.Background(), notify, nil, &atomic.Pointer[LogLevel]{}, nil, nil)

	EmitContent(ctx, json.RawMessage(`"req-1"`), Content{Type: "text", Text: "hello"})

	assert.Equal(t, DefaultContentChunkMethod, calledMethod)
	chunk, ok := calledParams.(ContentChunk)
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`"req-1"`), chunk.RequestID)
	assert.Equal(t, "text", chunk.Content.Type)
	assert.Equal(t, "hello", chunk.Content.Text)
}

// TestEmitContentNoOpWithoutSession verifies that EmitContent is safe to
// call outside a session context (e.g., in unit tests or background
// goroutines). It should be a no-op, not panic.
func TestEmitContentNoOpWithoutSession(t *testing.T) {
	// Should not panic
	EmitContent(context.Background(), json.RawMessage(`"req-1"`), Content{Type: "text", Text: "hello"})
}

// TestEmitContentNoOpWithNilNotify verifies that EmitContent is safe when
// the session context has a nil notify function (e.g., JSON response path
// where no SSE stream is available).
func TestEmitContentNoOpWithNilNotify(t *testing.T) {
	ctx := ContextWithSession(context.Background(), nil, nil, &atomic.Pointer[LogLevel]{}, nil, nil)
	// Should not panic
	EmitContent(ctx, json.RawMessage(`"req-1"`), Content{Type: "text", Text: "hello"})
}

// TestEmitContentCustomMethod verifies that WithContentChunkMethod
// overrides the default notification method name. This enables servers
// to configure a custom method for clients that expect a different name.
func TestEmitContentCustomMethod(t *testing.T) {
	var calledMethod string

	notify := NotifyFunc(func(method string, params any) {
		calledMethod = method
	})

	ctx := ContextWithSession(context.Background(), notify, nil, &atomic.Pointer[LogLevel]{}, nil, nil)
	ctx = WithContentChunkMethod(ctx, "custom/streaming")

	EmitContent(ctx, json.RawMessage(`"req-1"`), Content{Type: "text", Text: "hello"})

	assert.Equal(t, "custom/streaming", calledMethod)
}

// TestContentChunkMethodFromContextDefault verifies that the default
// method name is returned when no custom method is set on the context.
func TestContentChunkMethodFromContextDefault(t *testing.T) {
	assert.Equal(t, DefaultContentChunkMethod, ContentChunkMethodFromContext(context.Background()))
}

// TestContentChunkMethodFromContextCustom verifies that a custom method
// name set via WithContentChunkMethod is correctly retrieved.
func TestContentChunkMethodFromContextCustom(t *testing.T) {
	ctx := WithContentChunkMethod(context.Background(), "my/method")
	assert.Equal(t, "my/method", ContentChunkMethodFromContext(ctx))
}

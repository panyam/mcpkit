package core

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNotifyCollector captures notifications for assertion.
type testNotifyCollector struct {
	messages []LogMessage
}

func (c *testNotifyCollector) notify(method string, params any) {
	if msg, ok := params.(LogMessage); ok {
		c.messages = append(c.messages, msg)
	}
}

// newTestSlogContext creates a context with a session that captures log notifications.
func newTestSlogContext(level LogLevel) (context.Context, *testNotifyCollector) {
	collector := &testNotifyCollector{}
	var logLevel atomic.Pointer[LogLevel]
	logLevel.Store(&level)
	sc := &sessionCtx{
		notify:   collector.notify,
		logLevel: &logLevel,
	}
	ctx := context.WithValue(context.Background(), sessionCtxKey, sc)
	return ctx, collector
}

// TestMCPLogHandler_InfoRoutesToNotification verifies that slog.Info sends
// a notifications/message with MCP level "info" and the message in data.
func TestMCPLogHandler_InfoRoutesToNotification(t *testing.T) {
	ctx, collector := newTestSlogContext(LogDebug)
	logger := slog.New(NewMCPLogHandler(ctx, nil))

	logger.Info("processing request")

	require.Len(t, collector.messages, 1)
	assert.Equal(t, "info", collector.messages[0].Level)
	data := collector.messages[0].Data.(map[string]any)
	assert.Equal(t, "processing request", data["msg"])
}

// TestMCPLogHandler_LevelFiltering verifies that slog.Debug is dropped when
// the session's MCP log level is set to Info.
func TestMCPLogHandler_LevelFiltering(t *testing.T) {
	ctx, collector := newTestSlogContext(LogInfo) // only info and above
	logger := slog.New(NewMCPLogHandler(ctx, nil))

	logger.Debug("should be dropped")
	logger.Info("should arrive")

	require.Len(t, collector.messages, 1)
	assert.Equal(t, "info", collector.messages[0].Level)
}

// TestMCPLogHandler_Attrs verifies that slog attributes appear in the
// notification data alongside the message.
func TestMCPLogHandler_Attrs(t *testing.T) {
	ctx, collector := newTestSlogContext(LogDebug)
	logger := slog.New(NewMCPLogHandler(ctx, nil))

	logger.Info("found item", "key", "abc", "count", 42)

	require.Len(t, collector.messages, 1)
	data := collector.messages[0].Data.(map[string]any)
	assert.Equal(t, "found item", data["msg"])
	assert.Equal(t, "abc", data["key"])
	assert.Equal(t, int64(42), data["count"])
}

// TestMCPLogHandler_WithAttrs verifies that pre-set attributes from
// WithAttrs are included in every subsequent log record.
func TestMCPLogHandler_WithAttrs(t *testing.T) {
	ctx, collector := newTestSlogContext(LogDebug)
	base := NewMCPLogHandler(ctx, nil)
	logger := slog.New(base.WithAttrs([]slog.Attr{slog.String("service", "auth")}))

	logger.Info("started")

	require.Len(t, collector.messages, 1)
	data := collector.messages[0].Data.(map[string]any)
	assert.Equal(t, "auth", data["service"])
	assert.Equal(t, "started", data["msg"])
}

// TestMCPLogHandler_WithGroup verifies that WithGroup nests subsequent
// attributes under the group name.
func TestMCPLogHandler_WithGroup(t *testing.T) {
	ctx, collector := newTestSlogContext(LogDebug)
	base := NewMCPLogHandler(ctx, nil)
	logger := slog.New(base.WithGroup("req"))

	logger.Info("handled", "method", "GET", "path", "/api")

	require.Len(t, collector.messages, 1)
	data := collector.messages[0].Data.(map[string]any)
	assert.Equal(t, "handled", data["msg"])
	reqGroup := data["req"].(map[string]any)
	assert.Equal(t, "GET", reqGroup["method"])
	assert.Equal(t, "/api", reqGroup["path"])
}

// TestMCPLogHandler_SlogLevelMapping verifies the slog→MCP level mapping
// for all standard slog levels.
func TestMCPLogHandler_SlogLevelMapping(t *testing.T) {
	tests := []struct {
		slogLevel slog.Level
		mcpLevel  LogLevel
	}{
		{slog.LevelDebug, LogDebug},
		{slog.LevelInfo, LogInfo},
		{slog.LevelWarn, LogWarning},
		{slog.LevelError, LogError},
		{slog.LevelError + 4, LogCritical},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.mcpLevel, SlogToMCPLevel(tt.slogLevel),
			"slog.Level(%d) should map to %s", tt.slogLevel, tt.mcpLevel)
	}
}

// TestMCPLogHandler_MCPToSlogMapping verifies the reverse MCP→slog level
// mapping for round-trip correctness.
func TestMCPLogHandler_MCPToSlogMapping(t *testing.T) {
	tests := []struct {
		mcpLevel  LogLevel
		slogLevel slog.Level
	}{
		{LogDebug, slog.LevelDebug},
		{LogInfo, slog.LevelInfo},
		{LogNotice, slog.LevelInfo},
		{LogWarning, slog.LevelWarn},
		{LogError, slog.LevelError},
		{LogCritical, slog.LevelError + 4},
		{LogAlert, slog.LevelError + 4},
		{LogEmergency, slog.LevelError + 4},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.slogLevel, MCPToSlogLevel(tt.mcpLevel),
			"MCP %s should map to slog.Level(%d)", tt.mcpLevel, tt.slogLevel)
	}
}

// TestMCPLogHandler_NilSession verifies that the handler is a safe no-op
// when the context has no MCP session.
func TestMCPLogHandler_NilSession(t *testing.T) {
	logger := slog.New(NewMCPLogHandler(context.Background(), nil))

	// Should not panic.
	assert.False(t, logger.Handler().Enabled(context.Background(), slog.LevelInfo))
	logger.Info("should be silently dropped")
}

// TestMCPLogHandler_LoggerName verifies that the MCPLogHandlerOptions.Logger
// field is sent in the notifications/message "logger" field.
func TestMCPLogHandler_LoggerName(t *testing.T) {
	ctx, collector := newTestSlogContext(LogDebug)
	logger := slog.New(NewMCPLogHandler(ctx, &MCPLogHandlerOptions{Logger: "auth-service"}))

	logger.Info("check")

	require.Len(t, collector.messages, 1)
	assert.Equal(t, "auth-service", collector.messages[0].Logger)
}

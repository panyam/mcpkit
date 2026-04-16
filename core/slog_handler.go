package core

import (
	"context"
	"log/slog"
)

// MCPLogHandler implements slog.Handler, routing structured log records
// through MCP's notifications/message protocol to the connected client.
//
// The handler respects the per-session logging/setLevel — records below
// the client's requested level are dropped silently. slog levels are
// mapped to MCP LogLevel: Debug→debug, Info→info, Warn→warning,
// Error→error, >Error→critical.
//
// Create via NewMCPLogHandler inside a tool/resource/prompt handler:
//
//	func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
//	    logger := slog.New(core.NewMCPLogHandler(ctx, nil))
//	    logger.Info("processing", "key", "value")  // → notifications/message
//	    ...
//	}
type MCPLogHandler struct {
	sc     *sessionCtx
	logger string       // MCP "logger" field in notifications/message
	level  slog.Leveler // optional slog-level filter (nil = use session level only)
	attrs  []slog.Attr  // pre-set attributes from WithAttrs
	groups []string     // active group nesting from WithGroup
}

// MCPLogHandlerOptions configures NewMCPLogHandler.
type MCPLogHandlerOptions struct {
	// Logger is the MCP logger name sent in the "logger" field of
	// notifications/message. Defaults to "" (empty).
	Logger string

	// Level sets the minimum slog level. Records below this are dropped
	// before the MCP session level check. If nil, only the session's
	// logging/setLevel threshold applies.
	Level slog.Leveler
}

// NewMCPLogHandler creates an slog.Handler that sends log records as MCP
// notifications/message to the connected client. The ctx must carry an MCP
// session (as set by the dispatch layer in tool/resource/prompt handlers).
//
// If ctx has no session (e.g., called outside a handler), the handler is
// a safe no-op — Enabled() returns false for all levels.
func NewMCPLogHandler(ctx context.Context, opts *MCPLogHandlerOptions) *MCPLogHandler {
	h := &MCPLogHandler{sc: sessionFromContext(ctx)}
	if opts != nil {
		h.logger = opts.Logger
		h.level = opts.Level
	}
	return h
}

// Enabled reports whether the handler handles records at the given level.
// Returns false when: the slog level filter rejects it, the session has no
// logging enabled (client never called logging/setLevel), or the MCP session
// level threshold is above the mapped MCP level.
func (h *MCPLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	if h.level != nil && level < h.level.Level() {
		return false
	}
	if h.sc == nil || h.sc.logLevel == nil {
		return false
	}
	minLevel := h.sc.logLevel.Load()
	if minLevel == nil {
		return false
	}
	return SlogToMCPLevel(level) >= *minLevel
}

// Handle sends the slog record as an MCP notifications/message.
func (h *MCPLogHandler) Handle(_ context.Context, record slog.Record) error {
	if h.sc == nil || h.sc.notify == nil {
		return nil
	}
	mcpLevel := SlogToMCPLevel(record.Level)
	data := h.buildData(record)
	h.sc.notify("notifications/message", LogMessage{
		Level:  mcpLevel.String(),
		Logger: h.logger,
		Data:   data,
	})
	return nil
}

// WithAttrs returns a new handler with the given attributes pre-set.
func (h *MCPLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &MCPLogHandler{
		sc: h.sc, logger: h.logger, level: h.level,
		attrs: newAttrs, groups: h.groups,
	}
}

// WithGroup returns a new handler that nests subsequent attributes under
// the given group name.
func (h *MCPLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name
	return &MCPLogHandler{
		sc: h.sc, logger: h.logger, level: h.level,
		attrs: h.attrs, groups: newGroups,
	}
}

// buildData converts the slog record + pre-set attrs into a structured map
// suitable for the LogMessage.Data field.
func (h *MCPLogHandler) buildData(record slog.Record) any {
	m := make(map[string]any)
	m["msg"] = record.Message
	if !record.Time.IsZero() {
		m["time"] = record.Time.Format("2006-01-02T15:04:05.000Z07:00")
	}

	// Add pre-set attrs.
	target := m
	for _, g := range h.groups {
		sub := make(map[string]any)
		target[g] = sub
		target = sub
	}
	for _, a := range h.attrs {
		target[a.Key] = a.Value.Any()
	}

	// Add record attrs.
	record.Attrs(func(a slog.Attr) bool {
		target[a.Key] = a.Value.Any()
		return true
	})

	return m
}

// SlogToMCPLevel maps a slog.Level to an MCP LogLevel.
//
//	slog.LevelDebug  (-4) → LogDebug
//	slog.LevelInfo   (0)  → LogInfo
//	slog.LevelWarn   (4)  → LogWarning
//	slog.LevelError  (8)  → LogError
//	> slog.LevelError      → LogCritical
func SlogToMCPLevel(level slog.Level) LogLevel {
	switch {
	case level < slog.LevelInfo:
		return LogDebug
	case level < slog.LevelWarn:
		return LogInfo
	case level < slog.LevelError:
		return LogWarning
	case level == slog.LevelError:
		return LogError
	default:
		return LogCritical
	}
}

// MCPToSlogLevel maps an MCP LogLevel to the nearest slog.Level.
//
//	LogDebug, LogNotice → slog.LevelDebug / slog.LevelInfo
//	LogInfo             → slog.LevelInfo
//	LogWarning          → slog.LevelWarn
//	LogError            → slog.LevelError
//	LogCritical+        → slog.LevelError + 4
func MCPToSlogLevel(level LogLevel) slog.Level {
	switch level {
	case LogDebug:
		return slog.LevelDebug
	case LogInfo, LogNotice:
		return slog.LevelInfo
	case LogWarning:
		return slog.LevelWarn
	case LogError:
		return slog.LevelError
	default: // Critical, Alert, Emergency
		return slog.LevelError + 4
	}
}

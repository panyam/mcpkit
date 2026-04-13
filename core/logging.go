package core

import "context"

// LogLevel represents MCP log severity levels (syslog-based, ascending severity).
// Used with logging/setLevel to control the minimum level of log notifications
// sent to the client, and with EmitLog to specify the severity of a message.
type LogLevel int

const (
	LogDebug     LogLevel = iota // debug: detailed debugging information
	LogInfo                      // info: general informational messages
	LogNotice                    // notice: normal but significant events
	LogWarning                   // warning: warning conditions
	LogError                     // error: error conditions
	LogCritical                  // critical: critical conditions
	LogAlert                     // alert: action must be taken immediately
	LogEmergency                 // emergency: system is unusable
)

var logLevelNames = map[string]LogLevel{
	"debug":     LogDebug,
	"info":      LogInfo,
	"notice":    LogNotice,
	"warning":   LogWarning,
	"error":     LogError,
	"critical":  LogCritical,
	"alert":     LogAlert,
	"emergency": LogEmergency,
}

var logLevelStrings = [...]string{
	"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency",
}

// String returns the MCP wire name for the log level.
func (l LogLevel) String() string {
	if l >= LogDebug && int(l) < len(logLevelStrings) {
		return logLevelStrings[l]
	}
	return "debug"
}

// ParseLogLevel converts a string to a LogLevel.
// Returns the level and true on success, or (LogDebug, false) for unknown strings.
func ParseLogLevel(s string) (LogLevel, bool) {
	l, ok := logLevelNames[s]
	return l, ok
}

// LogMessage is the params payload for a notifications/message notification.
type LogMessage struct {
	Level  string `json:"level"`
	Logger string `json:"logger,omitempty"`
	Data   any    `json:"data"`
}

// EmitLog sends a notifications/message to the connected client if the session's
// log level allows it. Safe to call even if no session context is present (no-op).
//
// level is the severity of the message. logger is an optional logger name
// (typically the tool or subsystem name). data is the log payload (string, map, etc.).
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    mcpkit.EmitLog(ctx, mcpkit.LogInfo, "my-tool", "processing started")
//	    // ... do work ...
//	    return mcpkit.TextResult("done"), nil
//	}
func EmitLog(ctx context.Context, level LogLevel, logger string, data any) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil || sc.logLevel == nil {
		return
	}
	minLevel := sc.logLevel.Load()
	if minLevel == nil {
		// Logging not enabled for this session (client never called logging/setLevel)
		return
	}
	if level < *minLevel {
		return
	}
	sc.notify("notifications/message", LogMessage{
		Level:  level.String(),
		Logger: logger,
		Data:   data,
	})
}

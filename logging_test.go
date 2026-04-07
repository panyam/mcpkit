package mcpkit

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

// TestLogLevelParsing verifies that all 8 MCP log levels round-trip correctly
// between their string representation and LogLevel constant. This ensures the
// wire format matches what the conformance suite and MCP clients expect.
func TestLogLevelParsing(t *testing.T) {
	levels := []struct {
		name  string
		level LogLevel
	}{
		{"debug", LogDebug},
		{"info", LogInfo},
		{"notice", LogNotice},
		{"warning", LogWarning},
		{"error", LogError},
		{"critical", LogCritical},
		{"alert", LogAlert},
		{"emergency", LogEmergency},
	}

	for _, tc := range levels {
		t.Run(tc.name, func(t *testing.T) {
			// String → LogLevel
			got, ok := ParseLogLevel(tc.name)
			if !ok {
				t.Fatalf("ParseLogLevel(%q) returned false", tc.name)
			}
			if got != tc.level {
				t.Errorf("ParseLogLevel(%q) = %d, want %d", tc.name, got, tc.level)
			}

			// LogLevel → String
			if s := tc.level.String(); s != tc.name {
				t.Errorf("LogLevel(%d).String() = %q, want %q", tc.level, s, tc.name)
			}
		})
	}
}

// TestLogLevelParsingInvalid verifies that ParseLogLevel returns false for
// unknown level strings, preventing invalid levels from being accepted by
// the logging/setLevel handler.
func TestLogLevelParsingInvalid(t *testing.T) {
	for _, bad := range []string{"", "trace", "fatal", "DEBUG", "Info", "42"} {
		_, ok := ParseLogLevel(bad)
		if ok {
			t.Errorf("ParseLogLevel(%q) returned true, want false", bad)
		}
	}
}

// TestEmitLogFiltering verifies that EmitLog respects the session's minimum log
// level. Messages below the threshold are silently dropped; messages at or above
// the threshold are delivered via the NotifyFunc.
func TestEmitLogFiltering(t *testing.T) {
	var mu sync.Mutex
	var received []LogMessage

	notify := func(method string, params any) {
		if method != "notifications/message" {
			t.Errorf("unexpected method: %s", method)
			return
		}
		msg, ok := params.(LogMessage)
		if !ok {
			t.Errorf("unexpected params type: %T", params)
			return
		}
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	}

	// Set minimum level to warning
	var logLevel atomic.Pointer[LogLevel]
	warnLevel := LogWarning
	logLevel.Store(&warnLevel)

	ctx := contextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	// These should be filtered out (below warning)
	EmitLog(ctx, LogDebug, "test", "debug msg")
	EmitLog(ctx, LogInfo, "test", "info msg")
	EmitLog(ctx, LogNotice, "test", "notice msg")

	// These should be delivered (at or above warning)
	EmitLog(ctx, LogWarning, "test", "warning msg")
	EmitLog(ctx, LogError, "test", "error msg")
	EmitLog(ctx, LogCritical, "test", "critical msg")

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Fatalf("got %d messages, want 3 (warning, error, critical)", len(received))
	}
	wantLevels := []string{"warning", "error", "critical"}
	for i, want := range wantLevels {
		if received[i].Level != want {
			t.Errorf("received[%d].Level = %q, want %q", i, received[i].Level, want)
		}
	}
}

// TestEmitLogNoSession verifies that calling EmitLog with a plain context
// (no session context injected) is a safe no-op. This is important because
// tool handlers may be tested outside a transport context.
func TestEmitLogNoSession(t *testing.T) {
	// Should not panic
	EmitLog(context.Background(), LogInfo, "test", "should be dropped")
}

// TestEmitLogDisabled verifies that when the session exists but the client has
// not called logging/setLevel (logLevel pointer is nil), EmitLog is a no-op.
// This is the default state for new sessions.
func TestEmitLogDisabled(t *testing.T) {
	called := false
	notify := func(method string, params any) {
		called = true
	}

	var logLevel atomic.Pointer[LogLevel]
	// logLevel is nil (default) — logging disabled
	ctx := contextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitLog(ctx, LogEmergency, "test", "should be dropped even at emergency")

	if called {
		t.Error("NotifyFunc was called despite logging being disabled (nil logLevel)")
	}
}

// TestEmitLogAllLevelsPassAtDebug verifies that when the minimum level is set to
// debug (the lowest), all log messages are delivered regardless of severity.
func TestEmitLogAllLevelsPassAtDebug(t *testing.T) {
	count := 0
	notify := func(method string, params any) {
		count++
	}

	var logLevel atomic.Pointer[LogLevel]
	debugLevel := LogDebug
	logLevel.Store(&debugLevel)
	ctx := contextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitLog(ctx, LogDebug, "t", "d")
	EmitLog(ctx, LogInfo, "t", "i")
	EmitLog(ctx, LogEmergency, "t", "e")

	if count != 3 {
		t.Errorf("got %d notifications, want 3", count)
	}
}

// TestNotifyFunc verifies that the low-level Notify function correctly sends
// arbitrary server-to-client notifications through the session context.
func TestNotifyFunc(t *testing.T) {
	var gotMethod string
	var gotParams any
	notify := func(method string, params any) {
		gotMethod = method
		gotParams = params
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := contextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	ok := Notify(ctx, "notifications/progress", map[string]any{"token": "abc"})
	if !ok {
		t.Error("Notify returned false")
	}
	if gotMethod != "notifications/progress" {
		t.Errorf("method = %q, want notifications/progress", gotMethod)
	}
	if gotParams == nil {
		t.Error("params is nil")
	}
}

// TestNotifyNoSession verifies that Notify returns false when no session
// context is present, without panicking.
func TestNotifyNoSession(t *testing.T) {
	ok := Notify(context.Background(), "notifications/message", nil)
	if ok {
		t.Error("Notify returned true with no session context")
	}
}

// TestMarshalNotification verifies that marshalNotification produces valid
// JSON-RPC 2.0 notification objects (no "id" field, correct structure).
func TestMarshalNotification(t *testing.T) {
	raw, err := marshalNotification("notifications/message", LogMessage{
		Level:  "info",
		Logger: "test",
		Data:   "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}

	if obj["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", obj["jsonrpc"])
	}
	if obj["method"] != "notifications/message" {
		t.Errorf("method = %v, want notifications/message", obj["method"])
	}
	if _, hasID := obj["id"]; hasID {
		t.Error("notification should not have an id field")
	}
	params, ok := obj["params"].(map[string]any)
	if !ok {
		t.Fatal("params not a map")
	}
	if params["level"] != "info" {
		t.Errorf("params.level = %v, want info", params["level"])
	}
}

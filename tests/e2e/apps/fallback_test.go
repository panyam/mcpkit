package apps_test

import (
	"testing"
)

// TestToolResultHasText verifies that tools with UI metadata still return
// text content in their result. This is critical for text-only fallback —
// clients that don't support UI should still get a useful text response.
func TestToolResultHasText(t *testing.T) {
	c := setupConformanceClient(t)

	// show-dashboard has _meta.ui but should return text
	text, err := c.ToolCall("show-dashboard", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text == "" {
		t.Error("tool with UI metadata should still return text content")
	}
	if text != "Dashboard displayed" {
		t.Errorf("text = %q, want 'Dashboard displayed'", text)
	}
}

// TestMutationNotifiesResourceChange verifies that a tool calling
// NotifyResourcesChanged(ctx) sends the notifications/resources/list_changed
// notification to the client. Hosts use this to know when to re-fetch
// resources/list after a state-mutating tool call.
func TestMutationNotifiesResourceChange(t *testing.T) {
	c := setupConformanceClient(t)

	// The notification callback is set on the client in setupConformanceClient
	// but we need a fresh client with notification tracking for this test
	notifCh := make(chan string, 10)
	c = setupConformanceClientWithNotify(t, func(method string, params any) {
		notifCh <- method
	})

	_, err := c.ToolCall("mutate-dashboard", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}

	// Check notification was received
	select {
	case method := <-notifCh:
		if method != "notifications/resources/list_changed" {
			t.Errorf("notification method = %q, want %q", method, "notifications/resources/list_changed")
		}
	default:
		t.Error("expected notifications/resources/list_changed notification, got none")
	}
}

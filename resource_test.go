package mcpkit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// testResourceDispatcher creates an initialized dispatcher with test resources.
func testResourceDispatcher() *Dispatcher {
	d := NewDispatcher(ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterResource(
		ResourceDef{URI: "test://doc", Name: "Test Doc", MimeType: "text/plain"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "hello from resource",
			}}}, nil
		},
	)
	d.RegisterResource(
		ResourceDef{URI: "test://binary", Name: "Binary", MimeType: "application/octet-stream"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: req.URI, MimeType: "application/octet-stream", Blob: "AQID",
			}}}, nil
		},
	)
	d.RegisterResourceTemplate(
		ResourceTemplate{URITemplate: "test://items/{id}", Name: "Item", MimeType: "text/plain"},
		func(ctx context.Context, uri string, params map[string]string) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: uri, MimeType: "text/plain", Text: "item " + params["id"],
			}}}, nil
		},
	)
	initDispatcher(d)
	return d
}

// TestResourcesList verifies that resources/list returns all registered resources
// in registration order.
func TestResourcesList(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result struct {
		Resources []ResourceDef `json:"resources"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Resources) != 2 {
		t.Fatalf("got %d resources, want 2", len(result.Resources))
	}
	if result.Resources[0].URI != "test://doc" {
		t.Errorf("first resource URI = %q, want test://doc", result.Resources[0].URI)
	}
}

// TestResourcesListEmpty verifies that resources/list returns an empty list
// when no resources are registered.
func TestResourcesListEmpty(t *testing.T) {
	d := NewDispatcher(ServerInfo{Name: "test", Version: "1.0"})
	initDispatcher(d)
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result struct {
		Resources []ResourceDef `json:"resources"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Resources) != 0 {
		t.Errorf("got %d resources, want 0", len(result.Resources))
	}
}

// TestResourcesRead verifies that resources/read returns text content for a known URI.
func TestResourcesRead(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result ResourceResult
	json.Unmarshal(resp.Result, &result)
	if len(result.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(result.Contents))
	}
	if result.Contents[0].Text != "hello from resource" {
		t.Errorf("text = %q, want hello from resource", result.Contents[0].Text)
	}
}

// TestResourcesReadBinary verifies that resources/read returns blob content.
func TestResourcesReadBinary(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://binary"}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result ResourceResult
	json.Unmarshal(resp.Result, &result)
	if result.Contents[0].Blob != "AQID" {
		t.Errorf("blob = %q, want AQID", result.Contents[0].Blob)
	}
}

// TestResourcesReadUnknown verifies that resources/read returns an error for
// an unknown URI.
func TestResourcesReadUnknown(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://nonexistent"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected error for unknown resource")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeInvalidParams)
	}
}

// TestResourcesTemplatesList verifies that resources/templates/list returns
// registered templates.
func TestResourcesTemplatesList(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/templates/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result struct {
		ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.ResourceTemplates) != 1 {
		t.Fatalf("got %d templates, want 1", len(result.ResourceTemplates))
	}
	if result.ResourceTemplates[0].URITemplate != "test://items/{id}" {
		t.Errorf("template = %q", result.ResourceTemplates[0].URITemplate)
	}
}

// TestResourcesTemplateRead verifies that resources/read resolves a URI against
// a registered template and returns the parameterized content.
func TestResourcesTemplateRead(t *testing.T) {
	d := testResourceDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://items/42"}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result ResourceResult
	json.Unmarshal(resp.Result, &result)
	if result.Contents[0].Text != "item 42" {
		t.Errorf("text = %q, want item 42", result.Contents[0].Text)
	}
}

// TestResourcesCapabilities verifies that the initialize response includes
// resources capability when resources are registered.
func TestResourcesCapabilities(t *testing.T) {
	d := testResourceDispatcher()
	// Re-initialize to check capabilities (testResourceDispatcher already initializes)
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["resources"]; !ok {
		t.Error("capabilities missing 'resources'")
	}
}

// --- Resource Subscription Tests ---

// testSubscriptionDispatcher creates an initialized dispatcher with subscription
// support enabled, a subscription registry, and a captured notifyFunc. Returns
// the dispatcher, the Server (for calling NotifyResourceUpdated), and a channel
// that receives (method, params) tuples for each notification sent.
func testSubscriptionDispatcher() (*Dispatcher, *Server) {
	srv := NewServer(ServerInfo{Name: "test", Version: "1.0"}, WithSubscriptions())
	srv.RegisterResource(
		ResourceDef{URI: "test://doc", Name: "Test Doc", MimeType: "text/plain"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "hello",
			}}}, nil
		},
	)
	d := srv.newSession()
	d.sessionID = "test-session"
	initDispatcher(d)
	return d, srv
}

// TestResourcesSubscribe verifies that resources/subscribe returns an empty
// result object when subscribing to a known resource URI.
func TestResourcesSubscribe(t *testing.T) {
	d, _ := testSubscriptionDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	if resp.Error != nil {
		t.Fatalf("resources/subscribe error: %s", resp.Error.Message)
	}
	// Result should be an empty object {}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}

// TestResourcesUnsubscribe verifies that resources/unsubscribe returns an empty
// result object when unsubscribing from a URI.
func TestResourcesUnsubscribe(t *testing.T) {
	d, _ := testSubscriptionDispatcher()
	// Subscribe first
	d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	// Unsubscribe
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "resources/unsubscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	if resp.Error != nil {
		t.Fatalf("resources/unsubscribe error: %s", resp.Error.Message)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}

// TestResourcesSubscribeNotInitialized verifies that resources/subscribe returns
// an error when the server has not been initialized yet (init gating).
func TestResourcesSubscribeNotInitialized(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "test", Version: "1.0"}, WithSubscriptions())
	srv.RegisterResource(
		ResourceDef{URI: "test://doc", Name: "Doc"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{}, nil
		},
	)
	d := srv.newSession()
	// Do NOT call initDispatcher — session is not initialized
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected error for subscribe before initialization")
	}
	if resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeInvalidRequest)
	}
}

// TestResourcesSubscribeCapabilities verifies that when subscriptions are enabled,
// the initialize response includes "subscribe": true in the resources capability.
func TestResourcesSubscribeCapabilities(t *testing.T) {
	d, _ := testSubscriptionDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	caps := result["capabilities"].(map[string]any)
	resCap, ok := caps["resources"].(map[string]any)
	if !ok {
		t.Fatal("capabilities missing 'resources'")
	}
	sub, ok := resCap["subscribe"]
	if !ok {
		t.Fatal("resources capability missing 'subscribe' key")
	}
	if sub != true {
		t.Errorf("subscribe = %v, want true", sub)
	}
}

// TestResourcesSubscribeCapabilitiesDisabled verifies that when subscriptions are
// NOT enabled, the resources capability does not contain "subscribe".
func TestResourcesSubscribeCapabilitiesDisabled(t *testing.T) {
	d := testResourceDispatcher() // uses default dispatcher without subscriptions
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	caps := result["capabilities"].(map[string]any)
	resCap, ok := caps["resources"].(map[string]any)
	if !ok {
		t.Fatal("capabilities missing 'resources'")
	}
	if _, ok := resCap["subscribe"]; ok {
		t.Error("resources capability should NOT have 'subscribe' when disabled")
	}
}

// TestResourcesSubscribeNotification verifies the full subscription notification
// flow: subscribe to a resource URI, trigger NotifyResourceUpdated from the server,
// and verify the notifyFunc receives a notifications/resources/updated notification
// with the correct URI.
func TestResourcesSubscribeNotification(t *testing.T) {
	d, srv := testSubscriptionDispatcher()

	// Capture notifications
	var mu sync.Mutex
	var notifications []struct {
		method string
		params any
	}
	d.notifyFunc = func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		notifications = append(notifications, struct {
			method string
			params any
		}{method, params})
	}

	// Subscribe
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})
	if resp.Error != nil {
		t.Fatalf("subscribe error: %s", resp.Error.Message)
	}

	// Trigger notification from server
	srv.NotifyResourceUpdated("test://doc")

	// Verify notification was sent
	mu.Lock()
	defer mu.Unlock()
	if len(notifications) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifications))
	}
	if notifications[0].method != "notifications/resources/updated" {
		t.Errorf("method = %q, want notifications/resources/updated", notifications[0].method)
	}
	n, ok := notifications[0].params.(ResourceUpdatedNotification)
	if !ok {
		t.Fatalf("params type = %T, want ResourceUpdatedNotification", notifications[0].params)
	}
	if n.URI != "test://doc" {
		t.Errorf("notification URI = %q, want test://doc", n.URI)
	}
}

// TestResourcesUnsubscribeStopsNotification verifies that after unsubscribing,
// the session no longer receives notifications for the resource URI.
func TestResourcesUnsubscribeStopsNotification(t *testing.T) {
	d, srv := testSubscriptionDispatcher()

	var mu sync.Mutex
	var count int
	d.notifyFunc = func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		count++
	}

	// Subscribe
	d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})

	// Unsubscribe
	d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "resources/unsubscribe",
		Params: json.RawMessage(`{"uri":"test://doc"}`),
	})

	// Trigger — should NOT deliver
	srv.NotifyResourceUpdated("test://doc")

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Errorf("got %d notifications after unsubscribe, want 0", count)
	}
}

// TestResourcesSubscribeMultipleSessions verifies that when multiple sessions
// subscribe to the same URI, all of them receive the update notification when
// the server triggers NotifyResourceUpdated.
func TestResourcesSubscribeMultipleSessions(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "test", Version: "1.0"}, WithSubscriptions())
	srv.RegisterResource(
		ResourceDef{URI: "test://shared", Name: "Shared"},
		func(ctx context.Context, req ResourceRequest) (ResourceResult, error) {
			return ResourceResult{Contents: []ResourceReadContent{{
				URI: req.URI, Text: "shared",
			}}}, nil
		},
	)

	// Create two sessions
	d1 := srv.newSession()
	d1.sessionID = "session-1"
	initDispatcher(d1)

	d2 := srv.newSession()
	d2.sessionID = "session-2"
	initDispatcher(d2)

	var mu sync.Mutex
	counts := map[string]int{}

	d1.notifyFunc = func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		counts["session-1"]++
	}
	d2.notifyFunc = func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		counts["session-2"]++
	}

	// Both subscribe
	d1.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://shared"}`),
	})
	d2.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://shared"}`),
	})

	// Trigger
	srv.NotifyResourceUpdated("test://shared")

	// Small sleep to allow any async processing (though this is synchronous)
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if counts["session-1"] != 1 {
		t.Errorf("session-1 got %d notifications, want 1", counts["session-1"])
	}
	if counts["session-2"] != 1 {
		t.Errorf("session-2 got %d notifications, want 1", counts["session-2"])
	}
}

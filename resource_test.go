package mcpkit

import (
	"context"
	"encoding/json"
	"testing"
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

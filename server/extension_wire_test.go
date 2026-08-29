package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// settingsExtension declares settings the way SEP-2640 does, so the wire
// assertions below exercise a real settings object rather than an empty one.
type settingsExtension struct{}

func (settingsExtension) Extension() core.Extension {
	return core.Extension{
		ID:       "io.example/settings",
		Settings: map[string]any{"directoryRead": true},
	}
}

// bareExtension declares no settings, which is the common case: five of
// mcpkit's six extensions are shaped this way.
type bareExtension struct{}

func (bareExtension) Extension() core.Extension {
	return core.Extension{ID: "io.example/bare"}
}

// extensionsFromWire pulls capabilities.extensions out of raw response JSON,
// deliberately going through a generic map so the assertions see the bytes a
// client would rather than a typed round-trip that could hide an envelope.
func extensionsFromWire(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Capabilities struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode capabilities: %v (raw=%s)", err, raw)
	}
	if envelope.Capabilities.Extensions == nil {
		t.Fatalf("capabilities.extensions absent: %s", raw)
	}
	return envelope.Capabilities.Extensions
}

func assertInlineSettings(t *testing.T, exts map[string]any) {
	t.Helper()

	withSettings, ok := exts["io.example/settings"].(map[string]any)
	if !ok {
		t.Fatalf("io.example/settings = %#v, want an object", exts["io.example/settings"])
	}
	if withSettings["directoryRead"] != true {
		t.Errorf("directoryRead = %v, want true at the top level of the settings object", withSettings["directoryRead"])
	}
	for _, envelopeKey := range []string{"config", "specVersion", "stability", "id"} {
		if _, present := withSettings[envelopeKey]; present {
			t.Errorf("settings object carries %q; SEP-2133 defines no envelope", envelopeKey)
		}
	}

	bare, ok := exts["io.example/bare"].(map[string]any)
	if !ok {
		t.Fatalf("io.example/bare = %#v, want the empty object per SEP-2133", exts["io.example/bare"])
	}
	if len(bare) != 0 {
		t.Errorf("settings-free extension = %v, want {}", bare)
	}
}

// TestInitializeAdvertisesInlineExtensionSettings covers the legacy initialize
// handshake, one of two independent places that render core.Extension onto the
// wire.
func TestInitializeAdvertisesInlineExtensionSettings(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.extensions["io.example/settings"] = settingsExtension{}.Extension()
	d.extensions["io.example/bare"] = bareExtension{}.Extension()

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  core.NewRawJSON(json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}`)),
	})
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	assertInlineSettings(t, extensionsFromWire(t, raw))
}

// TestServerDiscoverAdvertisesInlineExtensionSettings covers the SEP-2575
// stateless wire. It builds its capabilities separately from the initialize
// path, so a fix applied to only one of the two would pass the other's test.
func TestServerDiscoverAdvertisesInlineExtensionSettings(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	s.RegisterExtension(settingsExtension{})
	s.RegisterExtension(bareExtension{})

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover",
		"params": validMetaParams(),
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body=%s", resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error != nil {
		t.Fatalf("discover error: %+v", r.Error)
	}

	raw, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	assertInlineSettings(t, extensionsFromWire(t, raw))
}

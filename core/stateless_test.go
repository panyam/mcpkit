package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeRequestMeta_MissingMeta(t *testing.T) {
	cases := []struct {
		name      string
		params    string
		wantField string
	}{
		{"empty params", ``, "_meta"},
		{"no _meta key", `{"name":"x"}`, "_meta"},
		{"null _meta", `{"_meta":null}`, "_meta"},
		{"empty _meta object", `{"_meta":{}}`, "protocolVersion"},
		{"unparseable params", `not json`, "_meta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.params != "" {
				raw = json.RawMessage(tc.params)
			}
			meta, err := DecodeRequestMeta(raw)
			if err == nil {
				t.Fatalf("expected MetaValidationError, got nil (meta=%+v)", meta)
			}
			var ve *MetaValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *MetaValidationError, got %T: %v", err, err)
			}
			if ve.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", ve.Field, tc.wantField)
			}
		})
	}
}

func TestDecodeRequestMeta_MissingRequiredSubfields(t *testing.T) {
	cases := []struct {
		name      string
		meta      string
		wantField string
	}{
		{
			"missing protocolVersion",
			`{
				"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},
				"io.modelcontextprotocol/clientCapabilities":{}
			}`,
			"protocolVersion",
		},
		{
			"missing clientCapabilities",
			`{
				"io.modelcontextprotocol/protocolVersion":"2026-07-28",
				"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"}
			}`,
			"clientCapabilities",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := []byte(`{"_meta":` + tc.meta + `}`)
			meta, err := DecodeRequestMeta(params)
			if err == nil {
				t.Fatalf("expected error for %s, got meta=%+v", tc.name, meta)
			}
			var ve *MetaValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *MetaValidationError, got %T", err)
			}
			if ve.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", ve.Field, tc.wantField)
			}
		})
	}
}

// TestDecodeRequestMeta_ClientInfoOptional locks the spec PR 3002 demotion:
// a request whose _meta omits clientInfo MUST decode cleanly (clientInfo is
// a SHOULD, never a rejection), with RequestMeta.ClientInfo left nil.
func TestDecodeRequestMeta_ClientInfoOptional(t *testing.T) {
	params := []byte(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": {}
		}
	}`)
	meta, err := DecodeRequestMeta(params)
	if err != nil {
		t.Fatalf("clientInfo-less envelope rejected: %v", err)
	}
	if meta.ClientInfo != nil {
		t.Errorf("ClientInfo = %+v, want nil for omitted field", meta.ClientInfo)
	}
}

func TestInjectServerInfoIntoResult(t *testing.T) {
	info := ServerInfo{Name: "srv", Version: "1.0"}

	readMeta := func(t *testing.T, result any) *ServerInfo {
		t.Helper()
		raw, err := MarshalJSON(result)
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			Meta ResultMeta `json:"_meta"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatal(err)
		}
		return probe.Meta.ServerInfo
	}

	t.Run("stamps plain object result", func(t *testing.T) {
		out := InjectServerInfoIntoResult(map[string]any{"tools": []any{}}, info)
		si := readMeta(t, out)
		if si == nil || si.Name != "srv" || si.Version != "1.0" {
			t.Errorf("serverInfo = %+v, want srv/1.0", si)
		}
	})

	t.Run("preserves existing _meta keys", func(t *testing.T) {
		out := InjectServerInfoIntoResult(map[string]any{
			"_meta": map[string]any{"vendor/x": "y"},
		}, info)
		raw, _ := MarshalJSON(out)
		var probe struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatal(err)
		}
		if _, ok := probe.Meta["vendor/x"]; !ok {
			t.Errorf("existing _meta key dropped: %s", raw)
		}
		if _, ok := probe.Meta[MetaKeyServerInfo]; !ok {
			t.Errorf("serverInfo not stamped alongside existing keys: %s", raw)
		}
	})

	t.Run("caller-set serverInfo wins", func(t *testing.T) {
		out := InjectServerInfoIntoResult(map[string]any{
			"_meta": map[string]any{
				MetaKeyServerInfo: map[string]any{"name": "custom", "version": "9"},
			},
		}, info)
		si := readMeta(t, out)
		if si == nil || si.Name != "custom" {
			t.Errorf("serverInfo = %+v, want caller-set custom", si)
		}
	})

	t.Run("empty info is a no-op", func(t *testing.T) {
		in := map[string]any{"a": 1}
		out := InjectServerInfoIntoResult(in, ServerInfo{})
		if si := readMeta(t, out); si != nil {
			t.Errorf("serverInfo = %+v, want none for empty identity", si)
		}
	})

	t.Run("non-object result unchanged", func(t *testing.T) {
		out := InjectServerInfoIntoResult([]string{"a"}, info)
		raw, _ := MarshalJSON(out)
		if string(raw) != `["a"]` {
			t.Errorf("non-object result mutated: %s", raw)
		}
	})
}

// TestClientCaps_StatelessWire verifies that ctx.ClientCaps() returns the
// per-request capability envelope set by the SEP-2575 stateless dispatcher.
// The cap-gating capability-check conformance scenario relies on this.
func TestClientCaps_StatelessWire(t *testing.T) {
	meta := &RequestMeta{
		ProtocolVersion:    DraftProtocolVersion2026V1,
		ClientInfo:         &ClientInfo{Name: "x", Version: "1"},
		ClientCapabilities: &ClientCapabilities{Sampling: &struct{}{}},
	}
	ctx := WithRequestMeta(context.Background(), meta)
	tc := NewToolContext(ctx)
	caps := tc.ClientCaps()
	if caps == nil {
		t.Fatal("ClientCaps() = nil, want per-request caps from envelope")
	}
	if caps.Sampling == nil {
		t.Errorf("Sampling = nil, want declared")
	}
	if caps.Elicitation != nil {
		t.Errorf("Elicitation = %+v, want nil (not declared)", caps.Elicitation)
	}
	// Symmetric check on PromptContext.
	pc := NewPromptContext(ctx)
	if pc.ClientCaps() != caps {
		t.Errorf("PromptContext.ClientCaps mismatch with ToolContext.ClientCaps")
	}
}

// TestClientCaps_BareContext_NilSafe documents the no-session, no-meta
// fallback: a bare ctx.Background() yields nil caps without panic.
func TestClientCaps_BareContext_NilSafe(t *testing.T) {
	tc := NewToolContext(context.Background())
	if got := tc.ClientCaps(); got != nil {
		t.Errorf("ClientCaps() on bare ctx = %+v, want nil", got)
	}
}

func TestDecodeRequestMeta_ValidEnvelope(t *testing.T) {
	params := []byte(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": {"name":"conformance-client","version":"1.0.0"},
			"io.modelcontextprotocol/clientCapabilities": {"sampling": {}}
		},
		"name": "some_tool"
	}`)
	meta, err := DecodeRequestMeta(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.ProtocolVersion != DraftProtocolVersion2026V1 {
		t.Errorf("ProtocolVersion = %q, want %q", meta.ProtocolVersion, DraftProtocolVersion2026V1)
	}
	if meta.ClientInfo == nil || meta.ClientInfo.Name != "conformance-client" {
		t.Errorf("ClientInfo = %+v, want name=conformance-client", meta.ClientInfo)
	}
	if meta.ClientCapabilities == nil {
		t.Fatal("ClientCapabilities = nil, want non-nil")
	}
}

func TestDecodeRequestMeta_LogLevelOptIn(t *testing.T) {
	params := []byte(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": {"name":"c","version":"1"},
			"io.modelcontextprotocol/clientCapabilities": {},
			"io.modelcontextprotocol/logLevel": "info"
		}
	}`)
	meta, err := DecodeRequestMeta(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", meta.LogLevel, "info")
	}
}

func TestSupportedStatelessVersionsListsDraft(t *testing.T) {
	if len(SupportedStatelessVersions) == 0 {
		t.Fatal("SupportedStatelessVersions is empty")
	}
	found := false
	for _, v := range SupportedStatelessVersions {
		if v == DraftProtocolVersion2026V1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DraftProtocolVersion2026V1 (%q) missing from SupportedStatelessVersions: %v",
			DraftProtocolVersion2026V1, SupportedStatelessVersions)
	}
}

func TestErrorPayloadShapes(t *testing.T) {
	// Wire shape verification — payload field names must match the
	// SEP-2575 conformance suite's JSON path expectations.
	t.Run("UnsupportedProtocolVersionData", func(t *testing.T) {
		d := UnsupportedProtocolVersionData{
			Supported: []string{"2026-07-28", "2025-11-25"},
			Requested: "1900-01-01",
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["supported"]; !ok {
			t.Errorf(`payload missing "supported" key; got %s`, b)
		}
		if m["requested"] != "1900-01-01" {
			t.Errorf(`payload "requested" wrong; got %s`, b)
		}
	})

	t.Run("MissingRequiredClientCapabilityData", func(t *testing.T) {
		// Object shape per upstream schema (not string-array). Mirrors a
		// ClientCapabilities value the client can merge and retry with.
		d := MissingRequiredClientCapabilityData{
			RequiredCapabilities: ClientCapabilities{
				Elicitation: &ElicitationCap{Form: &ElicitationFormCap{}},
			},
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		req, ok := m["requiredCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf(`requiredCapabilities not an object; got %s`, b)
		}
		if _, ok := req["elicitation"]; !ok {
			t.Errorf(`requiredCapabilities missing "elicitation" sub-key; got %s`, b)
		}
	})

	// HeaderMismatchData was removed during the merge with main — the
	// SEP-2243 path's generic map shape (server/header_validation.go)
	// is canonical for both routing-header and version-header mismatches.
}

func TestErrorCodeConstants(t *testing.T) {
	// Lock the numeric values — wire codes are part of the public contract.
	// Renumbered per modelcontextprotocol/modelcontextprotocol#2907.
	if ErrCodeHeaderMismatch != -32020 {
		t.Errorf("ErrCodeHeaderMismatch = %d, want -32020", ErrCodeHeaderMismatch)
	}
	if ErrCodeMissingRequiredClientCapability != -32021 {
		t.Errorf("ErrCodeMissingRequiredClientCapability = %d, want -32021", ErrCodeMissingRequiredClientCapability)
	}
	if ErrCodeUnsupportedProtocolVersion != -32022 {
		t.Errorf("ErrCodeUnsupportedProtocolVersion = %d, want -32022", ErrCodeUnsupportedProtocolVersion)
	}
}

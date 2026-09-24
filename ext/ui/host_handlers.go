package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// View→Host requests that are host capabilities rather than MCP server calls.
// AppHost answers these itself from HostHandlers and never forwards them to
// the server, which has no idea what they are.
const (
	MethodOpenLink           = "ui/open-link"
	MethodDownloadFile       = "ui/download-file"
	MethodMessage            = "ui/message"
	MethodRequestDisplayMode = "ui/request-display-mode"
	MethodUpdateModelContext = "ui/update-model-context"
)

// OpenLinkRequest is the params of ui/open-link.
type OpenLinkRequest struct {
	URL string `json:"url"`
}

// DownloadFileRequest is the params of ui/download-file (draft spec). Each
// entry is an EmbeddedResource or a ResourceLink; they stay raw because
// core.Content has no resource_link fields.
type DownloadFileRequest struct {
	Contents []json.RawMessage `json:"contents"`
}

// MessageRequest is the params of ui/message: content the app wants added to
// the conversation, which the host SHOULD treat as a follow-up turn.
type MessageRequest struct {
	Role    string         `json:"role"`
	Content []core.Content `json:"content"`
}

// UnmarshalJSON accepts content as either one block or an array. The spec's
// prose example shows a single object; the SDK schema uses an array.
func (m *MessageRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Content = nil
	c := bytes.TrimSpace(raw.Content)
	switch {
	case len(c) == 0 || bytes.Equal(c, []byte("null")):
	case c[0] == '{':
		var one core.Content
		if err := json.Unmarshal(c, &one); err != nil {
			return err
		}
		m.Content = []core.Content{one}
	default:
		if err := json.Unmarshal(c, &m.Content); err != nil {
			return err
		}
	}
	return nil
}

// DisplayModeRequest is the params of ui/request-display-mode.
type DisplayModeRequest struct {
	Mode string `json:"mode"` // "inline" | "fullscreen" | "pip"
}

// DisplayModeResult is the result of ui/request-display-mode: the mode the
// host actually set, which may differ from the one requested.
type DisplayModeResult struct {
	Mode string `json:"mode"`
}

// ModelContextUpdate is the params of ui/update-model-context. Each update
// replaces the previous one from the same view; it is a slot, not a log.
type ModelContextUpdate struct {
	Content           []core.Content `json:"content,omitempty"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
}

// DefaultDisplayMode is what ui/request-display-mode answers when the host has
// no RequestDisplayMode handler. The spec requires the host to return the mode
// in effect, and a host that cannot change modes stays inline.
const DefaultDisplayMode = "inline"

// HostHandlers supplies the host-side behaviour for View→Host requests that
// are not MCP server calls. A nil field means the host does not support that
// method, and the app gets -32601 rather than a forward to the server. The
// exception is RequestDisplayMode: when nil, the request is answered with
// DefaultDisplayMode, because the spec requires an answer.
//
// Handlers run on the bridge's request goroutine. Return a *HostError to
// control the JSON-RPC error the app sees; any other error becomes -32000,
// which is what the spec uses for "denied by user" and similar.
type HostHandlers struct {
	OpenLink           func(ctx context.Context, req OpenLinkRequest) error
	DownloadFile       func(ctx context.Context, req DownloadFileRequest) error
	Message            func(ctx context.Context, req MessageRequest) error
	RequestDisplayMode func(ctx context.Context, req DisplayModeRequest) (DisplayModeResult, error)
	UpdateModelContext func(ctx context.Context, update ModelContextUpdate) error

	// Notification receives every app→host notification AppHost does not
	// consume itself (it consumes notifications/tools/list_changed), e.g.
	// ui/notifications/size-changed, ui/notifications/request-teardown and
	// notifications/message.
	Notification func(method string, params json.RawMessage)
}

// HostError is a JSON-RPC error a HostHandlers func returns to pick the code
// the app sees.
type HostError struct {
	Code    int
	Message string
}

func (e *HostError) Error() string { return e.Message }

// WithHostHandlers installs the host-side handlers for ui/* requests.
func WithHostHandlers(h HostHandlers) AppHostOption {
	return func(ah *AppHost) { ah.handlers = h }
}

// isHostMethod reports whether method is answered by the host rather than
// forwarded to the MCP server, regardless of whether a handler is installed.
func isHostMethod(method string) bool {
	switch method {
	case MethodOpenLink, MethodDownloadFile, MethodMessage,
		MethodRequestDisplayMode, MethodUpdateModelContext:
		return true
	}
	return false
}

// handleHostRequest dispatches a host-capability request to HostHandlers.
func (h *AppHost) handleHostRequest(ctx context.Context, req *core.Request) *core.Response {
	result, err := h.dispatchHost(ctx, req)
	if err != nil {
		rpcErr := &core.Error{Code: core.ErrCodeServerError, Message: err.Error()}
		var he *HostError
		if errors.As(err, &he) {
			rpcErr.Code = he.Code
		}
		return &core.Response{ID: req.ID, Error: rpcErr}
	}
	if result == nil {
		result = struct{}{}
	}
	return &core.Response{ID: req.ID, Result: result}
}

func (h *AppHost) dispatchHost(ctx context.Context, req *core.Request) (any, error) {
	hh := h.handlers
	notFound := &HostError{Code: core.ErrCodeMethodNotFound, Message: "host does not support " + req.Method}

	switch req.Method {
	case MethodOpenLink:
		if hh.OpenLink == nil {
			return nil, notFound
		}
		var p OpenLinkRequest
		if err := decodeHostParams(req, &p); err != nil {
			return nil, err
		}
		return nil, hh.OpenLink(ctx, p)

	case MethodDownloadFile:
		if hh.DownloadFile == nil {
			return nil, notFound
		}
		var p DownloadFileRequest
		if err := decodeHostParams(req, &p); err != nil {
			return nil, err
		}
		return nil, hh.DownloadFile(ctx, p)

	case MethodMessage:
		if hh.Message == nil {
			return nil, notFound
		}
		var p MessageRequest
		if err := decodeHostParams(req, &p); err != nil {
			return nil, err
		}
		return nil, hh.Message(ctx, p)

	case MethodRequestDisplayMode:
		var p DisplayModeRequest
		if err := decodeHostParams(req, &p); err != nil {
			return nil, err
		}
		if hh.RequestDisplayMode == nil {
			return DisplayModeResult{Mode: DefaultDisplayMode}, nil
		}
		res, err := hh.RequestDisplayMode(ctx, p)
		if err != nil {
			return nil, err
		}
		return res, nil

	case MethodUpdateModelContext:
		if hh.UpdateModelContext == nil {
			return nil, notFound
		}
		var p ModelContextUpdate
		if err := decodeHostParams(req, &p); err != nil {
			return nil, err
		}
		return nil, hh.UpdateModelContext(ctx, p)
	}
	return nil, notFound
}

func decodeHostParams(req *core.Request, v any) error {
	raw := req.Params.Raw()
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &HostError{Code: core.ErrCodeInvalidParams, Message: fmt.Sprintf("%s: %v", req.Method, err)}
	}
	return nil
}

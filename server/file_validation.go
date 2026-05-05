package server

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// validateFileInputArgs walks a tool's inputSchema for SEP-2356
// `x-mcp-file` properties and runs `core.ValidateFileInput` on every
// matching argument. Returns a `-32602` response with structured error
// data on the first failure, nil otherwise.
//
// Supports both shapes the schema helpers emit:
//
//   - single file:  `properties.<name>.x-mcp-file = <descriptor>`
//   - array of files: `properties.<name>.items.x-mcp-file = <descriptor>`
//
// The error data payload is the typed `Data()` method on the matching
// `core.FileTooLargeError` / `core.FileTypeNotAcceptedError` — frozen by
// `conformance/file-inputs/scenarios.test.ts` as the cross-impl contract.
func (d *Dispatcher) validateFileInputArgs(id json.RawMessage, inputSchema any, args json.RawMessage) *core.Response {
	schemaMap, ok := inputSchema.(map[string]any)
	if !ok {
		// Tools registered with typed schemas (jsonschema.Schema or other
		// non-map shapes) skip this hook — they're free to validate
		// themselves. Future enhancement: walk typed schemas via reflection.
		return nil
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}

	var argMap map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argMap)
	}
	if argMap == nil {
		return nil
	}

	for propName, propAny := range props {
		prop, ok := propAny.(map[string]any)
		if !ok {
			continue
		}

		// Single-file property.
		if desc := core.ExtractFileInputDescriptor(prop); desc != nil {
			uri, ok := argMap[propName].(string)
			if !ok {
				// Missing arg or wrong type — schema validation already
				// surfaced this; skip silently here.
				continue
			}
			if err := core.ValidateFileInput(uri, desc); err != nil {
				return d.fileInputErrorResponse(id, propName, err)
			}
			continue
		}

		// Array of files: items carries the descriptor.
		items, ok := prop["items"].(map[string]any)
		if !ok {
			continue
		}
		desc := core.ExtractFileInputDescriptor(items)
		if desc == nil {
			continue
		}
		arr, ok := argMap[propName].([]any)
		if !ok {
			continue
		}
		for i, item := range arr {
			uri, ok := item.(string)
			if !ok {
				continue
			}
			if err := core.ValidateFileInput(uri, desc); err != nil {
				return d.fileInputErrorResponse(id, fmt.Sprintf("%s[%d]", propName, i), err)
			}
		}
	}
	return nil
}

// fileInputErrorResponse builds the JSON-RPC -32602 envelope for a
// file-input validation failure. The `data` field carries the typed
// payload from `core.FileTooLargeError.Data()` /
// `core.FileTypeNotAcceptedError.Data()` — the exact shape asserted by
// the conformance suite.
//
// `field` is the JSON-Schema property path of the offending arg (e.g.
// "image" or "documents[2]"); attached to the error data so clients
// rendering rich error UX can highlight the specific input that failed.
func (d *Dispatcher) fileInputErrorResponse(id json.RawMessage, field string, err error) *core.Response {
	switch typed := err.(type) {
	case *core.FileTooLargeError:
		typed.Field = field
		return core.NewErrorResponseWithData(
			id,
			core.ErrCodeInvalidParams,
			fmt.Sprintf("file input %q exceeds size limit", field),
			typed.Data(),
		)
	case *core.FileTypeNotAcceptedError:
		typed.Field = field
		return core.NewErrorResponseWithData(
			id,
			core.ErrCodeInvalidParams,
			fmt.Sprintf("file input %q rejected: type not in accept list", field),
			typed.Data(),
		)
	default:
		// Decoder-level failures (malformed data URI, non-base64) — surface
		// as -32602 too, but without structured data since these aren't
		// part of the conformance contract.
		return core.NewErrorResponse(
			id,
			core.ErrCodeInvalidParams,
			fmt.Sprintf("file input %q invalid: %s", field, err.Error()),
		)
	}
}

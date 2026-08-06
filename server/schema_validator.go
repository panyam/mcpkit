package server

import (
	"io"

	core "github.com/panyam/mcpkit/core"
)

// Schema validation moved to core/ so both sides of the protocol can use it.
// The server validates inbound tool and prompt arguments; the client validates
// an elicitation response against the schema the server asked for. Duplicating
// the compiler in client/ was the alternative, and client must not import
// server (the dependency runs the other way).
//
// These aliases keep the existing server API and call sites unchanged.

// ValidationError is one violation reported by schema validation.
type ValidationError = core.ValidationError

// ValidationErrors is the payload returned in the error.data field of
// JSON-RPC -32602 responses when argument validation fails.
type ValidationErrors = core.ValidationErrors

// compiledSchema is the server-internal spelling of core.CompiledSchema.
type compiledSchema = core.CompiledSchema

// compileSchema compiles a JSON Schema value into a validator.
// See core.CompileSchema.
func compileSchema(schemaValue any) (*compiledSchema, error) {
	return core.CompileSchema(schemaValue)
}

// drain is a helper for tests that may need to exhaust a reader.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

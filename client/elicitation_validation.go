package client

import (
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
)

// Elicitation response validation.
//
// A server sends elicitation/create with a RequestedSchema describing the
// shape it needs. Nothing previously checked that what came back matched: a
// handler that asked for an integer could hand the server a string, and one
// that offered three enum values could receive a fourth. The server has no way
// to tell a conforming answer from a malformed one, because the client is the
// only party that saw both the schema and the user's input.
//
// Validation runs AFTER the SEP-1034 defaults merge, since a schema-declared
// default can be what satisfies a `required` property.
//
// Symmetric with the server, which validates inbound tool and prompt arguments
// against their advertised schemas and rejects with -32602. Both sides share
// the compiler in core.

// validateElicitationContent checks accepted elicitation content against the
// schema the server asked for. Returns nil when the content conforms, when
// there is no schema, or when the schema itself cannot be compiled.
//
// A malformed or uncompilable schema is deliberately NOT an error. The user
// answered in good faith and the defect is on the server's side, so refusing
// their input would punish the wrong party. This mirrors
// extractElicitationDefaults, which also skips rather than fails on a schema
// it cannot read.
func validateElicitationContent(rawSchema json.RawMessage, content map[string]any) *core.ValidationErrors {
	if len(rawSchema) == 0 {
		return nil
	}

	var schema any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return nil
	}

	compiled, err := core.CompileSchema(schema)
	if err != nil || compiled == nil {
		return nil
	}

	// An accepted elicitation with no content is an empty object, not null;
	// that distinction matters for a schema declaring `required`.
	if content == nil {
		content = map[string]any{}
	}
	return compiled.ValidateValue(content)
}

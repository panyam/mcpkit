package main

// Driver for the upstream `json-schema-2020-12-preservation` client scenario
// (conformance PR 335).
//
// The scenario's mock server advertises two tools: a focal
// `json_schema_2020_12_tool` whose inputSchema carries the full
// SEP-1613 / SEP-2106 vocabulary ($schema, $defs, $anchor, allOf/anyOf,
// if/then/else, additionalProperties), and a permissive `json_schema_echo`.
// It grades whether the client's internal parsing round-trips that schema
// intact: we call tools/list, then hand the observed inputSchema straight
// back through tools/call on the echo tool, and the scenario diffs it
// against its fixture.
//
// core.ToolDef.InputSchema is `any`, so unknown keywords survive
// deserialization untouched. The value is passed through verbatim here —
// no re-marshalling into a typed schema struct, which is exactly the
// mistake the scenario exists to catch.

import (
	"context"
	"log"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

const (
	jsonSchemaFocalTool = "json_schema_2020_12_tool"
	jsonSchemaEchoTool  = "json_schema_echo"
)

func driveJSONSchemaPreservation(c *client.Client, tools []core.ToolDef) {
	var observed any
	for _, t := range tools {
		if t.Name == jsonSchemaFocalTool {
			observed = t.InputSchema
			break
		}
	}
	if observed == nil {
		log.Printf("json-schema-2020-12-preservation: %q not in tools/list (%d tools); skipping echo",
			jsonSchemaFocalTool, len(tools))
		return
	}

	log.Printf("json-schema-2020-12-preservation: echoing %q inputSchema back via %q",
		jsonSchemaFocalTool, jsonSchemaEchoTool)
	if _, err := c.ToolCall(context.Background(), jsonSchemaEchoTool, map[string]any{
		"schema": observed,
	}); err != nil {
		log.Printf("tools/call %q: %v", jsonSchemaEchoTool, err)
	}
}

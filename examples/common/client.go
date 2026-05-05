package common

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/client"
)

// PrintRPCError formats a *client.RPCError for an error-path demo step,
// printing code / message / data on separate lines so the wire shape is
// visible in stdout (not just buried in the conformance suite).
//
// If wantReason is non-empty, it's compared against err.Data["reason"]
// (when err.Data is a JSON object) and a WARN line is printed on
// mismatch — useful for spec-validation demos where a regression in the
// data shape should surface in the demo run, not just in tests.
//
// Pass "" for wantReason when the demo just wants to render the error
// without asserting a specific reason field.
func PrintRPCError(err error, wantReason string) {
	if err == nil {
		fmt.Printf("    UNEXPECTED: no error returned; expected an RPC error\n")
		return
	}
	var rpc *client.RPCError
	if !errors.As(err, &rpc) {
		fmt.Printf("    transport error: %v\n", err)
		return
	}
	fmt.Printf("    error.code:    %d\n", rpc.Code)
	fmt.Printf("    error.message: %s\n", rpc.Message)
	if rpc.Data == nil {
		fmt.Printf("    error.data:    <none>\n")
		return
	}
	pretty, _ := json.MarshalIndent(rpc.Data, "      ", "  ")
	fmt.Printf("    error.data:    %s\n", string(pretty))
	if wantReason == "" {
		return
	}
	gotReason := ""
	if m, ok := rpc.Data.(map[string]any); ok {
		gotReason, _ = m["reason"].(string)
	}
	if gotReason != wantReason {
		fmt.Printf("    WARN: data.reason = %q, expected %q\n", gotReason, wantReason)
	}
}

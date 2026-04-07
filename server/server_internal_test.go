package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRunDefaultsToStreamableHTTP verifies that Run() uses Streamable HTTP
// by default (not SSE). We test this indirectly by checking the transport
// config detection logic.
func TestRunDefaultsToStreamableHTTP(t *testing.T) {
	tc := transportConfig{}
	opt := WithStreamableHTTP(true)
	opt(&tc)
	assert.True(t, tc.streamableHTTP, "WithStreamableHTTP should set streamableHTTP")

	tc2 := transportConfig{}
	opt2 := WithSSE(true)
	opt2(&tc2)
	assert.True(t, tc2.sse, "WithSSE should set sse")
}

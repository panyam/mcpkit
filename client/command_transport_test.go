package client_test

// Tests for CommandTransport: subprocess MCP server lifecycle, stdio
// communication, graceful shutdown, stderr capture, env passthrough, and
// client integration via WithCommandTransport.
//
// These tests compile cmd/testserver as a binary and spawn it with STDIO=1
// to get a real subprocess MCP server communicating over stdin/stdout.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testServerBin string
	buildOnce     sync.Once
	buildErr      error
)

// buildTestServer compiles cmd/testserver into a temporary binary. The binary
// is built once per test run and reused across all subtests to avoid repeated
// compilation overhead. Uses os.TempDir (not t.TempDir) so the binary survives
// across top-level test functions.
func buildTestServer(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		bin := filepath.Join(os.TempDir(), "mcpkit-testserver-"+filepath.Base(t.Name()))
		cmd := exec.Command("go", "build", "-o", bin, "../cmd/testserver")
		cmd.Stderr = os.Stderr
		buildErr = cmd.Run()
		if buildErr == nil {
			testServerBin = bin
		}
	})
	require.NoError(t, buildErr, "failed to build testserver binary")
	require.NotEmpty(t, testServerBin)
	return testServerBin
}

// TestCommandTransport_EchoTool verifies the full lifecycle: spawn a subprocess
// MCP server via CommandTransport, perform the initialize handshake, call a
// tool, verify the response, and close cleanly.
func TestCommandTransport_EchoTool(t *testing.T) {
	bin := buildTestServer(t)

	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect())

	result, err := c.ToolCall("test_simple_text", map[string]any{})
	require.NoError(t, err)
	assert.NotEmpty(t, result, "should get a response from the echo tool")

	require.NoError(t, c.Close())
}

// TestCommandTransport_GracefulShutdown verifies that Close() shuts down the
// subprocess gracefully (via stdin EOF + SIGTERM) within the default timeout,
// without needing to escalate to SIGKILL.
func TestCommandTransport_GracefulShutdown(t *testing.T) {
	bin := buildTestServer(t)

	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect())

	// Measure shutdown time — should be fast (stdin EOF causes server exit).
	start := time.Now()
	require.NoError(t, c.Close())
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 3*time.Second,
		"graceful shutdown should complete quickly, not wait for SIGKILL timeout")
}

// TestCommandTransport_StderrCapture verifies that subprocess stderr output is
// captured and accessible via the Stderr() method. The testserver logs a
// startup message to stderr when running in STDIO mode.
func TestCommandTransport_StderrCapture(t *testing.T) {
	bin := buildTestServer(t)

	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect())

	// Do a call to give the server time to log.
	_, _ = c.ToolCall("test_simple_text", map[string]any{})
	c.Close()

	stderr := transport.Stderr()
	assert.NotEmpty(t, stderr, "should capture stderr from the subprocess")
	assert.Contains(t, stderr, "stdio", "testserver logs 'stdio' on startup")
}

// TestCommandTransport_ProcessCrash verifies that when the subprocess exits
// unexpectedly (e.g., bad command), Connect or subsequent calls return an
// error with useful context.
func TestCommandTransport_ProcessCrash(t *testing.T) {
	transport := client.NewCommandTransport("false", nil)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)

	// Connect may fail (process exits before handshake completes) or
	// a subsequent call may fail — either way we should get an error.
	err := c.Connect()
	if err == nil {
		_, err = c.ToolCall("test_simple_text", map[string]any{})
	}
	assert.Error(t, err, "should get an error when process crashes")
}

// TestCommandTransport_EnvPassthrough verifies that environment variables set
// via WithEnv are passed to the subprocess. We use STDIO=1 itself as proof —
// if the env var wasn't passed, the server would start in HTTP mode and the
// stdio handshake would fail.
//
// Bug context: without STDIO=1, the testserver starts an HTTP listener and
// never writes Content-Length framed data to stdout. Previously this caused
// Connect() to block forever. The default 30s connect timeout for command
// transports now catches this automatically with a diagnostic error message.
// We use a shorter timeout here to keep the test fast.
func TestCommandTransport_EnvPassthrough(t *testing.T) {
	bin := buildTestServer(t)

	// Without STDIO=1, the server starts HTTP and stdin/stdout handshake fails.
	// The default connect timeout catches this — we shorten it for test speed.
	transport := client.NewCommandTransport(bin, nil)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
		client.WithConnectTimeout(3*time.Second),
	)
	err := c.Connect()
	assert.Error(t, err, "without STDIO=1, connect should fail")
	assert.Contains(t, err.Error(), "timed out",
		"error should indicate a timeout, not a cryptic pipe error")
	c.Close()

	// With STDIO=1, it should work (default timeout is plenty).
	transport2 := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
	)
	c2 := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport2),
	)
	require.NoError(t, c2.Connect(), "with STDIO=1, connect should succeed")
	c2.Close()
}

// TestCommandTransport_WithCommandTransportOption verifies the WithCommandTransport
// client option, which stores command config on the Client and creates a fresh
// CommandTransport on each Connect(). This is the recommended API for most users.
func TestCommandTransport_WithCommandTransportOption(t *testing.T) {
	bin := buildTestServer(t)

	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithCommandTransport(bin, nil,
			client.WithEnv("STDIO=1"),
		),
	)
	require.NoError(t, c.Connect())

	result, err := c.ToolCall("test_simple_text", map[string]any{})
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	require.NoError(t, c.Close())
}

// TestCommandTransport_WithStderr verifies that the WithStderr option tees
// subprocess stderr to a user-provided writer in addition to the internal
// capture buffer.
func TestCommandTransport_WithStderr(t *testing.T) {
	bin := buildTestServer(t)

	var userBuf strings.Builder
	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
		client.WithStderr(&userBuf),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect())
	_, _ = c.ToolCall("test_simple_text", map[string]any{})
	c.Close()

	// Both the internal buffer and the user writer should have stderr content.
	assert.NotEmpty(t, transport.Stderr(), "internal stderr buffer should have content")
	assert.NotEmpty(t, userBuf.String(), "user stderr writer should have content")
	assert.Equal(t, transport.Stderr(), userBuf.String(),
		"both stderr destinations should have identical content")
}

// TestCommandTransport_ShutdownTimeout verifies that WithShutdownTimeout
// controls how long Close() waits before escalating to SIGKILL.
func TestCommandTransport_ShutdownTimeout(t *testing.T) {
	bin := buildTestServer(t)

	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
		client.WithShutdownTimeout(100*time.Millisecond),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect())

	// Close should still complete quickly with a short timeout.
	start := time.Now()
	c.Close()
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 2*time.Second)
}

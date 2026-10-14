package client_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readLineWithin reads one line from r, failing the test after d.
func readLineWithin(t *testing.T, r *bufio.Reader, d time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		line, _ := r.ReadString('\n')
		ch <- line
	}()
	select {
	case line := <-ch:
		return line
	case <-time.After(d):
		t.Fatalf("nothing read within %v", d)
		return ""
	}
}

// The client speaks MCP stdio framing by default: one JSON message per line,
// which is what third-party stdio servers expect.
func TestStdioTransport_WritesNewlineDelimited(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	defer serverOut.Close()

	tr := client.NewStdioTransport(clientIn, clientOut)
	require.NoError(t, tr.Connect(t.Context()))

	go tr.Notify(t.Context(), &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})

	line := readLineWithin(t, bufio.NewReader(serverIn), 5*time.Second)
	require.True(t, strings.HasSuffix(line, "}\n"), "want one JSON object per line, got %q", line)
	var msg struct {
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &msg), "line is not JSON: %q", line)
	assert.Equal(t, "notifications/initialized", msg.Method)
}

// The #1517 repro against the real binary: a raw newline-delimited initialize,
// written the way Claude Code or a Python client would, gets a one-line answer.
func TestCommandServer_AnswersRawNewlineDelimitedInitialize(t *testing.T) {
	bin := buildTestServer(t)

	cmd := exec.Command(bin)
	cmd.Env = append(cmd.Environ(), "STDIO=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	_, err = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`+"\n")
	require.NoError(t, err)

	line := readLineWithin(t, bufio.NewReader(stdout), 10*time.Second)
	require.True(t, strings.HasPrefix(line, "{"), "want a JSON line, got %q", line)
	var resp core.Response
	require.NoError(t, json.Unmarshal([]byte(line), &resp))
	assert.Nil(t, resp.Error)
	assert.Equal(t, "1", string(resp.ID))
}

// FramingContentLength still works end to end: the current server detects it
// from the first frame and answers in kind.
func TestCommandTransport_ContentLengthFraming(t *testing.T) {
	bin := buildTestServer(t)

	transport := client.NewCommandTransport(bin, nil,
		client.WithEnv("STDIO=1"),
		client.WithCommandFraming(client.FramingContentLength),
	)
	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(transport),
	)
	require.NoError(t, c.Connect(t.Context()))
	defer c.Close()

	result, err := c.ToolCall(t.Context(), "test_simple_text", map[string]any{})
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

// WithIOTransport forwards framing options to the transport it builds.
func TestIOTransport_ContentLengthOption(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	defer serverOut.Close()

	c := client.NewClient("", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithIOTransport(clientIn, clientOut, client.WithStdioFraming(client.FramingContentLength)),
	)
	go c.Connect(t.Context())

	line := readLineWithin(t, bufio.NewReader(serverIn), 5*time.Second)
	assert.True(t, strings.HasPrefix(line, "Content-Length: "), "want a Content-Length header, got %q", line)
}

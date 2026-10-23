package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initializeOverStdio runs srv on a pipe and returns its initialize result's
// capabilities object, raw.
func initializeOverStdio(t *testing.T, srv *server.Server) map[string]json.RawMessage {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = srv.RunIO(ctx, inR, outW) }()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}` + "\n"
	_, err := inW.Write([]byte(req))
	require.NoError(t, err)
	line, err := bufio.NewReader(outR).ReadBytes('\n')
	require.NoError(t, err)

	var resp struct {
		Result struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(line, &resp))
	return resp.Result.Capabilities
}

func TestWithExperimentalCapability_DeclaredAtInitialize(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "t", Version: "0"},
		server.WithExperimentalCapability("claude/channel", nil),
		server.WithExperimentalCapability("example/feature", map[string]any{"level": 2}))
	caps := initializeOverStdio(t, srv)

	require.Contains(t, caps, "experimental")
	assert.JSONEq(t, `{"claude/channel":{},"example/feature":{"level":2}}`, string(caps["experimental"]))
}

func TestWithExperimentalCapability_AbsentByDefault(t *testing.T) {
	caps := initializeOverStdio(t, server.NewServer(core.ServerInfo{Name: "t", Version: "0"}))
	assert.NotContains(t, caps, "experimental")
}

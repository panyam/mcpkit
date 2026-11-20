// Command check-server-card fetches an MCP server's Server Card the way a host
// would before connecting, then connects and checks the card against what the
// server reports. It is the checker behind `make exercise-server-card`
// (mission #1737).
//
//	go run ./tools/check-server-card -url http://127.0.0.1:8080/mcp -wire legacy
//
// It prints one line per check and exits 1 if any fails.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/mcp", "the server's streamable HTTP URL")
	wire := flag.String("wire", "legacy", "how to connect for the comparison: legacy | stateless")
	schemaPath := flag.String("schema", "core/testdata/servercard/schema.json", "vendored v1 Server Card schema")
	flag.Parse()

	c := &checker{}
	c.run(*url, *wire, *schemaPath)
	if c.failed {
		os.Exit(1)
	}
}

type checker struct{ failed bool }

func (c *checker) check(name string, ok bool, detail string) {
	mark := "ok  "
	if !ok {
		mark = "FAIL"
		c.failed = true
	}
	fmt.Printf("  %s %s", mark, name)
	if detail != "" {
		fmt.Printf(": %s", detail)
	}
	fmt.Println()
}

func (c *checker) run(url, wire, schemaPath string) {
	cardURL := strings.TrimRight(url, "/") + core.ServerCardPath
	fmt.Printf("server card @ %s (compare over %s wire)\n", cardURL, wire)

	resp, body, err := get(cardURL, nil)
	if err != nil {
		c.check("GET card", false, err.Error())
		return
	}
	c.check("GET card returns 200", resp.StatusCode == http.StatusOK, fmt.Sprintf("HTTP %d", resp.StatusCode))
	c.check("Content-Type is "+core.ServerCardMediaType, resp.Header.Get("Content-Type") == core.ServerCardMediaType, resp.Header.Get("Content-Type"))
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers"} {
		c.check("CORS "+h, resp.Header.Get(h) != "", resp.Header.Get(h))
	}

	schema, err := loadSchema(schemaPath)
	if err != nil {
		c.check("load schema", false, err.Error())
	} else {
		verr := schema.Validate(body)
		detail := ""
		if verr != nil {
			b, _ := json.Marshal(verr.Errors)
			detail = string(b)
		}
		c.check("card validates against the v1 schema", verr == nil, detail)
	}

	var card core.ServerCard
	if err := json.Unmarshal(body, &card); err != nil {
		c.check("card decodes", false, err.Error())
		return
	}
	if err := card.Validate(); err != nil {
		c.check("card passes core.ServerCard.Validate", false, err.Error())
	}
	if len(card.Remotes) == 0 {
		c.check("card lists a remote", false, "")
		return
	}
	remote := card.Remotes[0]
	c.check("remote url is the endpoint", remote.URL == strings.TrimRight(url, "/"), remote.URL)

	etag := resp.Header.Get("ETag")
	again, _, err := get(cardURL, map[string]string{"If-None-Match": etag})
	c.check("If-None-Match gets 304", err == nil && etag != "" && again.StatusCode == http.StatusNotModified,
		fmt.Sprintf("ETag %s", etag))

	info, versions, err := runtimeIdentity(url, wire)
	if err != nil {
		c.check("connect over "+wire, false, err.Error())
		return
	}
	c.check("card version matches serverInfo", card.Version == info.Version, card.Version+" vs "+info.Version)
	c.check("card title matches serverInfo", card.Title == info.Title, card.Title+" vs "+info.Title)
	switch wire {
	case "stateless":
		c.check("supportedProtocolVersions match server/discover", slices.Equal(remote.SupportedProtocolVersions, versions),
			fmt.Sprintf("card %v, discover %v", remote.SupportedProtocolVersions, versions))
	default:
		c.check("negotiated version is on the card", slices.Contains(remote.SupportedProtocolVersions, versions[0]),
			fmt.Sprintf("negotiated %s, card %v", versions[0], remote.SupportedProtocolVersions))
	}
}

func get(url string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", core.ServerCardMediaType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

func loadSchema(path string) (*core.CompiledSchema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	// The upstream file holds only $defs, and its own example validator
	// checks against #/$defs/ServerCard.
	schema["$ref"] = "#/$defs/ServerCard"
	return core.CompileSchema(schema)
}

// runtimeIdentity connects the way a client would after reading the card. On
// the stateless wire it returns server/discover's versions. On the legacy wire
// it returns the one version initialize negotiated.
func runtimeIdentity(url, wire string) (core.Implementation, []string, error) {
	if wire == "stateless" {
		cl := client.NewClient(url, core.Implementation{Name: "check-server-card", Version: "1"},
			client.WithClientMode(client.ClientModeStateless))
		if err := cl.Connect(context.Background()); err != nil {
			return core.Implementation{}, nil, err
		}
		defer cl.Close()
		dr, err := cl.Discover()
		if err != nil {
			return core.Implementation{}, nil, err
		}
		return dr.ServerInfo, dr.SupportedVersions, nil
	}

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "check-server-card", "version": "1"},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return core.Implementation{}, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw = firstSSEData(raw)
	}
	var out struct {
		Result struct {
			ProtocolVersion string              `json:"protocolVersion"`
			ServerInfo      core.Implementation `json:"serverInfo"`
		} `json:"result"`
		Error *core.Error `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return core.Implementation{}, nil, fmt.Errorf("initialize: HTTP %d: %s", resp.StatusCode, raw)
	}
	if out.Error != nil {
		return core.Implementation{}, nil, fmt.Errorf("initialize: %s", out.Error.Message)
	}
	return out.Result.ServerInfo, []string{out.Result.ProtocolVersion}, nil
}

func firstSSEData(raw []byte) []byte {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return raw
}

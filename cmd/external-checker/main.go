// Command external-checker drives the mcpkit CLIENT against the third-party
// "stateless draft" conformance gauntlet at https://mcp-checker-2026-07-28.val.run
// and renders conformance/EXTERNAL_CHECKER.md.
//
// Unlike every testconf-* suite — which grades an mcpkit *server* with the
// upstream runner acting as the client — this driver inverts the roles: a real
// client.NewClient(..., ClientModeStateless) is the thing under test, and the
// remote val.town endpoint is the grader. It exercises the integrated stateless
// draft wire (2026-07-28 / DRAFT-2026-v1): SEP-2575 (per-request _meta +
// MCP-Protocol-Version header, no initialize), SEP-2243 (Mcp-Method / Mcp-Name
// routing headers), SEP-2106 ($ref argument construction), and SEP-2322 (the
// MRTR input_required round-trip).
//
// The endpoint is an external, version-pinned, ephemeral deployment, so this is
// run manually (just testconf-external-checker), not in the blocking CI path.
// The committed report is a point-in-time snapshot, like conformance/UPSTREAM_AUDIT.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

const (
	defaultURL = "https://mcp-checker-2026-07-28.val.run"
	protoVer   = "2026-07-28"
)

// check is one graded surface in the report.
type check struct {
	Name   string
	SEP    string
	Pass   bool
	Detail string // server-echoed verdict or failure reason
}

func main() {
	url := flag.String("url", defaultURL, "stateless-draft conformance endpoint")
	out := flag.String("out", "conformance/EXTERNAL_CHECKER.md", "report output path")
	flag.Parse()

	checks, serverInfo, runErr := run(*url)

	report := render(*url, serverInfo, checks)
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(2)
	}
	fmt.Printf("Wrote %s\n", *out)

	allPass := runErr == nil
	for _, c := range checks {
		status := "OK"
		if !c.Pass {
			status = "FAIL"
			allPass = false
		}
		fmt.Printf("  [%-4s] %-26s %s\n", status, c.Name, c.SEP)
	}
	if !allPass {
		os.Exit(1)
	}
}

// run drives the client and returns one check per graded surface.
func run(url string) (checks []check, serverInfo core.ServerInfo, fatal error) {
	c := client.NewClient(url, core.ClientInfo{Name: "mcpkit-external-checker", Version: "0.1"},
		client.WithClientMode(client.ClientModeStateless),
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{"confirmed": true}}, nil
		}),
	)

	// SEP-2575: Connect with no initialize handshake. The stateless wire stamps
	// MCP-Protocol-Version + _meta{protocolVersion,clientInfo,clientCapabilities}
	// on every POST; if any were missing the gauntlet rejects the request, so a
	// clean Connect already proves the transport envelope.
	if err := c.Connect(); err != nil {
		checks = append(checks, check{"Connect (stateless wire)", "SEP-2575", false, err.Error()})
		return checks, serverInfo, err
	}
	defer c.Close()
	serverInfo = c.ServerInfo
	checks = append(checks, check{"Connect (stateless wire)", "SEP-2575", true,
		"no initialize handshake; MCP-Protocol-Version header + _meta envelope accepted on every POST"})

	ctx := context.Background()

	// SEP-2575: clientCapabilities.elicitation must reach the server in _meta —
	// the gauntlet only lists mrtr_confirm when it does, otherwise it substitutes
	// an elicitation_missing placeholder.
	tools, err := c.ListTools(ctx)
	if err != nil {
		checks = append(checks, check{"clientCapabilities in _meta", "SEP-2575", false, err.Error()})
		return checks, serverInfo, err
	}
	names := map[string]bool{}
	for _, t := range tools {
		names[t.Name] = true
	}
	capPass := names["mrtr_confirm"] && !names["elicitation_missing"]
	capDetail := fmt.Sprintf("tools/list returned %s", strings.Join(keys(names), ", "))
	if !capPass {
		capDetail += " — elicitation capability not observed by server"
	}
	checks = append(checks, check{"clientCapabilities in _meta", "SEP-2575", capPass, capDetail})

	// SEP-2106: arguments honor the inputSchema — required string, a real JSON
	// number (not "42"), and a same-document $ref payload (#/$defs/payload).
	args, err := c.ToolCall("validate_arguments", map[string]any{
		"message": "hello-from-mcpkit",
		"count":   42,
		"payload": map[string]any{"kind": "solid"},
	})
	checks = append(checks, verdict("validate_arguments", "SEP-2106", args, err))

	// SEP-2322: MRTR input_required round-trip — answer the elicitation and
	// re-call with requestState echoed back unchanged. CallToolWithInputs +
	// DefaultInputHandler drives the loop.
	res, err := client.CallToolWithInputs(ctx, c, "mrtr_confirm", map[string]any{}, client.DefaultInputHandler(c))
	checks = append(checks, mrtrVerdict("mrtr_confirm (MRTR round-trip)", "SEP-2322", res, err))

	// SEP-2243: every successful call above carried Mcp-Method (and Mcp-Name on
	// the tool calls). The gauntlet rejects any POST missing the routing header,
	// so the green results above are themselves the proof. Recorded explicitly
	// for the report.
	routingPass := allPassed(checks)
	checks = append(checks, check{"Routing headers (Mcp-Method/Mcp-Name)", "SEP-2243", routingPass,
		"every graded request was accepted — a missing routing header is rejected before tool dispatch"})

	return checks, serverInfo, nil
}

func verdict(name, sep, text string, err error) check {
	if err != nil {
		return check{name, sep, false, err.Error()}
	}
	return check{name, sep, strings.Contains(text, "CONFORMANCE OK"), text}
}

func mrtrVerdict(name, sep string, res *client.ToolCallResult, err error) check {
	if err != nil {
		return check{name, sep, false, err.Error()}
	}
	if res == nil || res.Sync == nil {
		return check{name, sep, false, "no sync result returned from MRTR loop"}
	}
	text := ""
	for _, content := range res.Sync.Content {
		text += content.Text
	}
	return check{name, sep, strings.Contains(text, "CONFORMANCE OK"), text}
}

func allPassed(checks []check) bool {
	for _, c := range checks {
		if !c.Pass {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// deterministic order for stable report diffs
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func render(url string, info core.ServerInfo, checks []check) string {
	var b strings.Builder
	pass, total := 0, len(checks)
	for _, c := range checks {
		if c.Pass {
			pass++
		}
	}
	verdictWord := "PASS"
	if pass != total {
		verdictWord = "FAIL"
	}

	b.WriteString("# External Stateless-Draft Conformance\n\n")
	b.WriteString("Snapshot of the mcpkit **client** graded by the third-party stateless-draft gauntlet at\n")
	b.WriteString(fmt.Sprintf("[`%s`](%s) — an MCP server that judges every request its *client* sends.\n\n", url, url))
	b.WriteString(fmt.Sprintf("**Protocol version:** `%s` / `DRAFT-2026-v1` (this checker grades that version only)  \n", protoVer))
	b.WriteString("**Surface under test:** the mcpkit **client** (`client.NewClient(..., WithClientMode(ClientModeStateless))`)  \n")
	b.WriteString("**Driver:** `cmd/external-checker`  \n")
	if info.Name != "" {
		b.WriteString(fmt.Sprintf("**Grader:** `%s` v%s\n\n", info.Name, info.Version))
	} else {
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("**Verdict:** **%s** — %d / %d checks passed.\n\n", verdictWord, pass, total))

	b.WriteString("Unlike every `testconf-*` suite — which grades an mcpkit *server* with the upstream\n")
	b.WriteString("runner acting as the client — this report inverts the roles: a real mcpkit client is\n")
	b.WriteString("the thing under test and the remote endpoint is the grader. It is the only check that\n")
	b.WriteString("exercises the **integrated** stateless draft wire (SEP-2575 + 2243 + 2106 + 2322 enforced\n")
	b.WriteString("simultaneously, by an independent third party) from the client side.\n\n")

	b.WriteString("The endpoint is an external, version-pinned, ephemeral deployment, so this is a\n")
	b.WriteString("**point-in-time snapshot**, not a CI gate. Regenerate via `just testconf-external-checker`.\n\n")

	b.WriteString("## Results\n\n")
	b.WriteString("| Check | SEP | Result | Detail |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, c := range checks {
		mark := "✅ pass"
		if !c.Pass {
			mark = "❌ fail"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", c.Name, c.SEP, mark, sanitize(c.Detail)))
	}

	b.WriteString("\n## What each SEP requires of the client\n\n")
	b.WriteString("- **SEP-2575 (stateless wire):** `MCP-Protocol-Version` header on every POST; `_meta` carries\n")
	b.WriteString("  `io.modelcontextprotocol/protocolVersion`, `clientInfo`, and `clientCapabilities`; no `initialize` handshake.\n")
	b.WriteString("- **SEP-2243 (routing headers):** `Mcp-Method` mirrors the JSON-RPC method on every POST; `Mcp-Name` on tool calls.\n")
	b.WriteString("- **SEP-2106 ($ref arguments):** arguments honor the `inputSchema` — real JSON numbers (not stringified) and\n")
	b.WriteString("  same-document `$ref` payloads resolved by the caller.\n")
	b.WriteString("- **SEP-2322 (MRTR):** handle a `resultType: input_required` result, answer the elicitation with `{action, content}`,\n")
	b.WriteString("  and re-call echoing `requestState` back unchanged.\n\n")

	b.WriteString("## Known caveat\n\n")
	b.WriteString("`ClientModeStateless`'s `Connect()` treats `server/discover` as **mandatory** and fails fatally if the\n")
	b.WriteString("server omits it (`client/client.go`). This gauntlet implements `server/discover`, so the check passes — but\n")
	b.WriteString("the draft states a client may \"start with `server/discover` *or any request directly*.\" Against a conformant\n")
	b.WriteString("draft server that omits discover, mcpkit's stateless client cannot currently connect. Tracked as\n")
	b.WriteString("issue 829; it does not affect the grade above.\n")

	return b.String()
}

// sanitize keeps the detail cell single-line and table-safe.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

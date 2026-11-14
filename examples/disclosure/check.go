package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/groups"
	"github.com/panyam/mcpkit/ext/skills"
)

// Row names. The tests look rows up by these, and the exercise script prints
// them, so a rename shows up in both places.
const (
	rowPlainTools  = "plain client: tools/list returns every tool"
	rowPlainSkill  = "plain client: SKILL.md reads and verifies"
	rowGroupsList  = "groups client: groups/list returns one group per theme"
	rowGroupsGet   = "groups client: groups/get billing returns its tools and SKILL.md"
	rowGroupsTools = "groups client: tools/list leaves out grouped tools"
	rowSizes       = "sizes: groups/get billing vs flat tools/list"
)

// checkRow is one line of the check report. Info rows print a measurement and
// never fail.
type checkRow struct {
	Name   string
	OK     bool
	Info   bool
	Detail string
}

// runChecks connects two clients to url and checks what each one sees. The
// plain client declares no extensions. The groups client declares the groups
// extension. opts picks the wire for both.
func runChecks(ctx context.Context, url string, opts ...client.ClientOption) []checkRow {
	var rows []checkRow

	plain, err := connect(ctx, url, "disclosure-plain", opts...)
	if err != nil {
		return []checkRow{{Name: rowPlainTools, Detail: "connect: " + err.Error()}}
	}
	defer plain.Close()

	flatCount, flatBytes, err := listAllTools(ctx, plain)
	rows = append(rows, countRow(rowPlainTools, flatCount, flatBytes, err, func(n int) bool { return n == toolCount }))
	rows = append(rows, checkSkill(ctx, plain))

	groupsOpts := append([]client.ClientOption{groups.ClientOption()}, opts...)
	grouped, err := connect(ctx, url, "disclosure-groups", groupsOpts...)
	if err != nil {
		return append(rows, checkRow{Name: rowGroupsList, Detail: "connect: " + err.Error()})
	}
	defer grouped.Close()

	listRow, listBytes := checkGroupsList(ctx, grouped)
	rows = append(rows, listRow)
	getRow, getBytes := checkGroupsGet(ctx, grouped)
	rows = append(rows, getRow)

	n, b, err := listAllTools(ctx, grouped)
	// Every fixture tool is in a group, so a groups client sees none of them.
	rows = append(rows, countRow(rowGroupsTools, n, b, err, func(n int) bool { return n == 0 }))

	size := checkRow{Name: rowSizes, Info: true, Detail: fmt.Sprintf("flat tools/list %d bytes, groups/get n/a", flatBytes)}
	if getBytes > 0 && flatBytes > 0 {
		path := listBytes + getBytes + b
		size.Detail = fmt.Sprintf("groups/list %d + groups/get billing %d + tools/list %d = %d bytes, flat tools/list %d bytes (%d%%)",
			listBytes, getBytes, b, path, flatBytes, 100*path/flatBytes)
	}
	return append(rows, size)
}

func connect(ctx context.Context, url, name string, opts ...client.ClientOption) (*client.Client, error) {
	c := client.NewClient(url, core.ClientInfo{Name: name, Version: "0.1.0"}, opts...)
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// listAllTools pages through tools/list and returns the tool count and the
// total size of the result payloads, which is what a client would put into a
// model's context.
func listAllTools(ctx context.Context, c *client.Client) (int, int, error) {
	count, size := 0, 0
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.Call(ctx, "tools/list", params)
		if err != nil {
			return count, size, err
		}
		var lr core.ToolsListResult
		if err := res.Unmarshal(&lr); err != nil {
			return count, size, err
		}
		count += len(lr.Tools)
		size += len(res.Raw)
		if lr.NextCursor == "" {
			return count, size, nil
		}
		cursor = lr.NextCursor
	}
	return count, size, fmt.Errorf("tools/list did not finish in 50 pages")
}

func countRow(name string, n, size int, err error, ok func(int) bool) checkRow {
	if err != nil {
		return checkRow{Name: name, Detail: err.Error()}
	}
	return checkRow{Name: name, OK: ok(n), Detail: fmt.Sprintf("%d of %d tools, %d bytes", n, toolCount, size)}
}

func checkSkill(ctx context.Context, c *client.Client) checkRow {
	row := checkRow{Name: rowPlainSkill}
	sc := skills.NewClient(c)
	entries, err := sc.ListSkillEntries(ctx)
	if err != nil {
		row.Detail = err.Error()
		return row
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.URI, "/billing-refunds/SKILL.md") {
			continue
		}
		res, err := sc.ReadFromEntry(ctx, e, e.URI)
		if err != nil {
			row.Detail = err.Error()
			return row
		}
		row.OK = res.DigestVerified
		row.Detail = fmt.Sprintf("%s, %d bytes, digest verified %v", e.URI, len(res.Bytes), res.DigestVerified)
		return row
	}
	row.Detail = fmt.Sprintf("billing-refunds not in skills/list (%d entries)", len(entries))
	return row
}

// checkGroupsList returns the row and the size of the groups/list result, or
// 0 when the call failed.
func checkGroupsList(ctx context.Context, c *client.Client) (checkRow, int) {
	row := checkRow{Name: rowGroupsList}
	res, err := c.Call(ctx, groups.MethodList, map[string]any{})
	if err != nil {
		row.Detail = err.Error()
		return row, 0
	}
	var lr struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(res.Raw, &lr); err != nil {
		row.Detail = err.Error()
		return row, len(res.Raw)
	}
	for _, g := range lr.Groups {
		if _, isTool := g["inputSchema"]; isTool {
			row.Detail = fmt.Sprintf("entry %v carries an inputSchema, so it is a tool", g["name"])
			return row, len(res.Raw)
		}
	}
	row.OK = len(lr.Groups) == len(themes)
	row.Detail = fmt.Sprintf("%d groups, want %d, %d bytes", len(lr.Groups), len(themes), len(res.Raw))
	return row, len(res.Raw)
}

// checkGroupsGet returns the row and the size of the groups/get result, or 0
// when the call failed.
func checkGroupsGet(ctx context.Context, c *client.Client) (checkRow, int) {
	row := checkRow{Name: rowGroupsGet}
	res, err := c.Call(ctx, groups.MethodGet, map[string]any{"name": "billing"})
	if err != nil {
		row.Detail = err.Error()
		return row, 0
	}
	var gr struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res.Raw, &gr); err != nil {
		row.Detail = err.Error()
		return row, len(res.Raw)
	}
	for _, t := range gr.Tools {
		if !strings.HasPrefix(t.Name, "billing_") {
			row.Detail = "tool " + t.Name + " is not in billing"
			return row, len(res.Raw)
		}
	}
	hasSkill := strings.Contains(string(res.Raw), billingSkill)
	want := toolCount / len(themes)
	row.OK = len(gr.Tools) == want && hasSkill
	row.Detail = fmt.Sprintf("%d tools, want %d, SKILL.md listed %v", len(gr.Tools), want, hasSkill)
	return row, len(res.Raw)
}

// printRows writes the report and reports whether every non-info row passed.
func printRows(wire string, rows []checkRow) bool {
	ok := true
	for _, r := range rows {
		verdict := "PASS"
		switch {
		case r.Info:
			verdict = "INFO"
		case !r.OK:
			verdict = "FAIL"
			ok = false
		}
		fmt.Printf("  %-4s %-9s %s: %s\n", verdict, wire, r.Name, r.Detail)
	}
	return ok
}

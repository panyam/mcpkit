package groups

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// ClientOption returns the client.ClientOption that declares the extension.
// Pass it to client.NewClient. A client must declare the extension to have
// grouped primitives left out of its base lists; one that doesn't still gets
// the flat lists.
func ClientOption() client.ClientOption {
	return client.WithExtension(ExtensionID, core.ClientExtensionCap{})
}

// Client calls groups/list and groups/get on a connected client.
type Client struct {
	mcp *client.Client
}

// NewClient wraps c. Build c with ClientOption so the server scopes its
// lists for this caller.
func NewClient(c *client.Client) *Client {
	return &Client{mcp: c}
}

// List returns every top-level group, following nextCursor to the end.
// Nested groups are not here; read them from their parents with Get.
func (c *Client) List(ctx context.Context) ([]Group, error) {
	var out []Group
	cursor := ""
	for page := 0; page < 1000; page++ {
		res, err := c.mcp.Call(ctx, MethodList, ListGroupsRequest{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", MethodList, err)
		}
		var lr ListGroupsResult
		if err := res.Unmarshal(&lr); err != nil {
			return nil, fmt.Errorf("decode %s: %w", MethodList, err)
		}
		out = append(out, lr.Groups...)
		if lr.NextCursor == "" {
			return out, nil
		}
		cursor = lr.NextCursor
	}
	return nil, fmt.Errorf("%s: more than 1000 pages", MethodList)
}

// Get fetches one group's contents. An unknown name comes back as a
// *client.RPCError with code ErrCodeGroupNotFound.
func (c *Client) Get(ctx context.Context, name string) (GetGroupResult, error) {
	res, err := c.mcp.Call(ctx, MethodGet, GetGroupRequest{Name: name})
	if err != nil {
		return GetGroupResult{}, fmt.Errorf("%s %s: %w", MethodGet, name, err)
	}
	var gr GetGroupResult
	if err := res.Unmarshal(&gr); err != nil {
		return GetGroupResult{}, fmt.Errorf("decode %s: %w", MethodGet, err)
	}
	return gr, nil
}

// Walk fetches name and every group reachable from it, depth first, calling
// visit once per group. It skips a group it has already fetched, so a cycle
// ends the walk instead of looping, as the draft requires of clients.
func (c *Client) Walk(ctx context.Context, name string, visit func(name string, g GetGroupResult) error) error {
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(n string) error {
		if seen[n] {
			return nil
		}
		seen[n] = true
		g, err := c.Get(ctx, n)
		if err != nil {
			return err
		}
		if err := visit(n, g); err != nil {
			return err
		}
		for _, child := range g.Groups {
			if err := walk(child.Name); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(name)
}

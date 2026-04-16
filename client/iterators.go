package client

import (
	"context"
	"iter"

	core "github.com/panyam/mcpkit/core"
)

// Tools returns an iterator that yields all tool definitions, automatically
// paginating through multiple pages if the server uses cursor-based pagination.
//
// Example:
//
//	for tool, err := range c.Tools(ctx) {
//	    if err != nil { log.Fatal(err) }
//	    fmt.Println(tool.Name)
//	}
func (c *Client) Tools(ctx context.Context) iter.Seq2[core.ToolDef, error] {
	return paginate[core.ToolDef](ctx, c, "tools/list",
		func(raw *CallResult) ([]core.ToolDef, string, error) {
			var resp core.ToolsListResult
			if err := raw.Unmarshal(&resp); err != nil {
				return nil, "", err
			}
			return resp.Tools, resp.NextCursor, nil
		},
	)
}

// Resources returns an iterator that yields all resource definitions,
// automatically paginating through multiple pages.
func (c *Client) Resources(ctx context.Context) iter.Seq2[core.ResourceDef, error] {
	return paginate[core.ResourceDef](ctx, c, "resources/list",
		func(raw *CallResult) ([]core.ResourceDef, string, error) {
			var resp core.ResourcesListResult
			if err := raw.Unmarshal(&resp); err != nil {
				return nil, "", err
			}
			return resp.Resources, resp.NextCursor, nil
		},
	)
}

// ResourceTemplates returns an iterator that yields all resource template
// definitions, automatically paginating through multiple pages.
func (c *Client) ResourceTemplates(ctx context.Context) iter.Seq2[core.ResourceTemplate, error] {
	return paginate[core.ResourceTemplate](ctx, c, "resources/templates/list",
		func(raw *CallResult) ([]core.ResourceTemplate, string, error) {
			var resp core.ResourceTemplatesListResult
			if err := raw.Unmarshal(&resp); err != nil {
				return nil, "", err
			}
			return resp.ResourceTemplates, resp.NextCursor, nil
		},
	)
}

// Prompts returns an iterator that yields all prompt definitions,
// automatically paginating through multiple pages.
func (c *Client) Prompts(ctx context.Context) iter.Seq2[core.PromptDef, error] {
	return paginate[core.PromptDef](ctx, c, "prompts/list",
		func(raw *CallResult) ([]core.PromptDef, string, error) {
			var resp core.PromptsListResult
			if err := raw.Unmarshal(&resp); err != nil {
				return nil, "", err
			}
			return resp.Prompts, resp.NextCursor, nil
		},
	)
}

// paginate is a generic helper that produces an iterator over paginated
// MCP list results. It calls the given method with an optional cursor param,
// extracts items and nextCursor from the response, and yields each item.
// Stops when nextCursor is empty or the context is cancelled.
func paginate[T any](ctx context.Context, c *Client, method string,
	extract func(*CallResult) ([]T, string, error),
) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var cursor string
		for {
			// Build params with optional cursor.
			var params any
			if cursor != "" {
				params = map[string]string{"cursor": cursor}
			}

			result, err := c.Call(method, params)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}

			items, nextCursor, err := extract(result)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}

			for _, item := range items {
				if ctx.Err() != nil {
					return
				}
				if !yield(item, nil) {
					return
				}
			}

			if nextCursor == "" {
				return
			}
			cursor = nextCursor
		}
	}
}

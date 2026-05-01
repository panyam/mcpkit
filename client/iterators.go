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

// --- Single-page list helpers (SEP-2549 TTL accessible) ---
//
// The Tools/Resources/Prompts/ResourceTemplates iterators above are
// item-by-item — they discard the per-page envelope (NextCursor, TTL)
// once items have been yielded. The pre-existing zero-arg helpers
// (ListTools/ListResources/ListPrompts/ListResourceTemplates on
// client.go) likewise drop the envelope. Callers that need the
// SEP-2549 TTL hint to drive client-side caching should use the
// `ListXPage(cursor)` helpers below: each fetches ONE page and returns
// the typed result intact.
//
// Pagination cursor handling is the caller's responsibility — pass
// empty string for the first page; pass the previous response's
// NextCursor for subsequent pages; loop until NextCursor is empty.

// ListToolsPage fetches one page of tools/list and returns the typed
// result including SEP-2549 TTL and pagination cursor. Use Tools(ctx)
// for the auto-paginating item iterator when you don't need the envelope
// metadata, or the zero-arg ListTools() if you only want the items from
// the first page.
func (c *Client) ListToolsPage(cursor string) (*core.ToolsListResult, error) {
	var out core.ToolsListResult
	if err := callListPage(c, "tools/list", cursor, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListResourcesPage fetches one page of resources/list and returns the
// typed result including SEP-2549 TTL and pagination cursor.
func (c *Client) ListResourcesPage(cursor string) (*core.ResourcesListResult, error) {
	var out core.ResourcesListResult
	if err := callListPage(c, "resources/list", cursor, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListResourceTemplatesPage fetches one page of resources/templates/list
// and returns the typed result including SEP-2549 TTL and pagination cursor.
func (c *Client) ListResourceTemplatesPage(cursor string) (*core.ResourceTemplatesListResult, error) {
	var out core.ResourceTemplatesListResult
	if err := callListPage(c, "resources/templates/list", cursor, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPromptsPage fetches one page of prompts/list and returns the typed
// result including SEP-2549 TTL and pagination cursor.
func (c *Client) ListPromptsPage(cursor string) (*core.PromptsListResult, error) {
	var out core.PromptsListResult
	if err := callListPage(c, "prompts/list", cursor, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// callListPage is the shared dispatch path for the four ListXPage
// helpers — all four list endpoints take an optional cursor param and
// unmarshal into a typed result. Centralized so the cursor encoding
// stays consistent with what `paginate` (the iterator helper) sends.
func callListPage(c *Client, method, cursor string, out any) error {
	var params any
	if cursor != "" {
		params = map[string]string{"cursor": cursor}
	}
	result, err := c.Call(method, params)
	if err != nil {
		return err
	}
	return result.Unmarshal(out)
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

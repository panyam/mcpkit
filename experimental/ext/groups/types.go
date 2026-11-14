// Package groups is an experimental implementation of the groups extension,
// io.modelcontextprotocol/groups, drafted by the Progressive Disclosure
// working group (progressive-disclosure-wg PR 19, pinned at 15b2a5cf).
//
// A server sorts its tools, prompts and resources into named groups. A
// client that declares the extension sees one short entry per group in
// groups/list, fetches a group's contents with groups/get when the model
// picks it, and no longer sees grouped primitives in tools/list,
// prompts/list or resources/list. A client that doesn't declare it sees the
// flat lists, as before.
//
// The draft has no sponsor and its shapes can change. Server side, build a
// Catalog and call RegisterWith after registering the primitives it names.
// Client side, wrap a *client.Client with NewClient.
package groups

import (
	"github.com/panyam/mcpkit/core"
)

// ExtensionID is the capability key both sides declare.
const ExtensionID = "io.modelcontextprotocol/groups"

// Method names. The draft settled on the plural form in
// progressive-disclosure-wg PR 23.
const (
	MethodList = "groups/list"
	MethodGet  = "groups/get"
)

// ErrCodeGroupNotFound is the error code for groups/get on a name the server
// doesn't serve. The draft reuses -32002, the code resources/read uses for an
// unknown URI.
const ErrCodeGroupNotFound = -32002

// Group is the summary of a group, as groups/list returns it and as a
// parent's groups/get lists its children. It never carries the group's
// contents.
type Group struct {
	// Name identifies the group in groups/get. It is 1 to 64 characters of
	// lowercase letters, digits and single hyphens, with no hyphen at either
	// end.
	Name string `json:"name"`
	// Title is an optional display name for a host UI.
	Title string `json:"title,omitempty"`
	// Description tells the model when to pick the group.
	Description string         `json:"description"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// ListGroupsRequest is the params of groups/list.
type ListGroupsRequest struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListGroupsResult is the result of groups/list. Only groups without a parent
// appear here; nested groups appear in their parents' groups/get.
type ListGroupsResult struct {
	Groups     []Group `json:"groups"`
	NextCursor string  `json:"nextCursor,omitempty"`
	TTLMs      *int    `json:"ttlMs,omitempty"`
	CacheScope string  `json:"cacheScope,omitempty"`
}

// GetGroupRequest is the params of groups/get.
type GetGroupRequest struct {
	Name string `json:"name"`
}

// GetGroupResult is the result of groups/get. Tools, prompts and resources
// are complete definitions, ready for tools/call, prompts/get and
// resources/read. Groups lists child groups by summary only; each child takes
// its own groups/get.
type GetGroupResult struct {
	Instructions string             `json:"instructions,omitempty"`
	Tools        []core.ToolDef     `json:"tools,omitempty"`
	Prompts      []core.PromptDef   `json:"prompts,omitempty"`
	Resources    []core.ResourceDef `json:"resources,omitempty"`
	Groups       []Group            `json:"groups,omitempty"`
	TTLMs        *int               `json:"ttlMs,omitempty"`
	CacheScope   string             `json:"cacheScope,omitempty"`
}

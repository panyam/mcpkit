package mcpkit

import "context"

// PromptDef describes a prompt exposed via MCP.
type PromptDef struct {
	// Name is the prompt identifier used in prompts/get.
	Name string `json:"name"`

	// Title is an optional display title.
	Title string `json:"title,omitempty"`

	// Description explains what this prompt does.
	Description string `json:"description,omitempty"`

	// Arguments defines the parameters this prompt accepts.
	Arguments []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument describes a single argument to a prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is a single message in a prompt result.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"` // reuses Content from tool.go
}

// PromptRequest is the validated input passed to a PromptHandler.
type PromptRequest struct {
	Name      string
	Arguments map[string]string
}

// PromptResult is the response from a prompt handler.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptHandler generates prompt messages, optionally using arguments.
type PromptHandler func(ctx context.Context, req PromptRequest) (PromptResult, error)

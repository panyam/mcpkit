package core

import "context"

// CompletionRef identifies what is being completed — a prompt argument or resource URI.
type CompletionRef struct {
	// Type is "ref/prompt" for prompt argument completion or "ref/resource" for resource URI completion.
	Type string `json:"type"`

	// Name is the prompt name (when Type is "ref/prompt").
	Name string `json:"name,omitempty"`

	// URI is the resource URI template (when Type is "ref/resource").
	URI string `json:"uri,omitempty"`
}

// CompletionArgument describes the argument being completed and the partial input so far.
type CompletionArgument struct {
	// Name is the argument name being completed.
	Name string `json:"name"`

	// Value is the partial input the user has typed so far.
	Value string `json:"value"`
}

// CompletionResult is the server's response with completion suggestions.
type CompletionResult struct {
	// Values is the list of completion suggestions.
	Values []string `json:"values"`

	// Total is the total number of available completions (may be larger than len(Values)).
	Total int `json:"total,omitempty"`

	// HasMore indicates there are additional completions beyond what was returned.
	HasMore bool `json:"hasMore"`
}

// CompletionCompleteResult is the typed result for completion/complete responses.
type CompletionCompleteResult struct {
	Completion CompletionResult `json:"completion"`
}

// CompletionHandler provides autocompletion suggestions for a specific reference.
// ref identifies the prompt or resource being completed, arg contains the argument
// name and partial value. Return matching suggestions.
type CompletionHandler func(ctx context.Context, ref CompletionRef, arg CompletionArgument) (CompletionResult, error)

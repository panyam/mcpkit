package files

import (
	"context"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
)

// Extension contributes the file tools and the prompt that tells a model how
// to drive them.
//
// The two travel together because neither works alone. The tools refuse an
// edit whose anchor is not unique, and a model that has not been told that
// will keep sending single-word anchors and keep being refused. Registering
// the tools without the prompt would ship a mechanism whose contract is
// discoverable only by failing.
type Extension struct {
	host.BaseExtension
	src *Source
}

// New builds the extension. Root confines every path the tools will touch and
// is required; see Config.
func New(cfg Config) (*Extension, error) {
	src, err := NewSource(cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{src: src}, nil
}

// Name identifies the extension, and is the source id its tools register
// under.
func (e *Extension) Name() string { return "files" }

// Tools returns read_file and edit_file.
func (e *Extension) Tools() (agent.ToolSource, error) { return e.src, nil }

// PromptSections states the edit discipline the tools enforce.
func (e *Extension) PromptSections() []host.PromptSection {
	return []host.PromptSection{host.PromptSectionFunc(func(context.Context) string {
		return `## Editing files

Read a file before editing it, and pass the hash read_file returned as expect_hash.
An edit whose expect_hash no longer matches is refused, because the file changed
after you read it and your edit was written against content that is gone. Read it
again and redo the edit against what is there now.

Each edit replaces an exact snippet. The text in ` + "`old`" + ` must appear exactly once in
the file and is matched byte for byte, including indentation. If it appears more
than once the edit is refused rather than applied to a guess: add surrounding
lines until it is unique. All edits in one call apply together or none do.`
	})}
}

var _ host.Extension = (*Extension)(nil)

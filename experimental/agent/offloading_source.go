package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// ReadToolResultName is the reserved tool name OffloadingSource injects
// so the model can fetch an offloaded result. A wrapped source that also
// exposes this name is shadowed by the offloader's handler.
const ReadToolResultName = "read_tool_result"

// DefaultOffloadThreshold is the model-visible size (bytes of flattened
// result text) at or above which a successful tool result is offloaded.
// 4 KB keeps ordinary results inline while catching the file dumps and
// long stdout that actually bloat context.
const DefaultOffloadThreshold = 4096

// DefaultOffloadPreview is how many leading characters of the flattened
// result the stub carries inline, so the model can often act without a
// read_tool_result round-trip at all.
const DefaultOffloadPreview = 400

// defaultReadLimit bounds a read_tool_result window when the caller does
// not set limit — large enough to be useful, small enough to stay a
// window rather than re-inlining the whole payload.
const defaultReadLimit = 4000

// OffloadConfig tunes an OffloadingSource. The zero value is usable
// (default threshold and preview); only Store is required and is set by
// the constructor, not here.
type OffloadConfig struct {
	// Threshold is the offload cutoff in bytes of flattened result text.
	// Zero means DefaultOffloadThreshold. Results below it inline
	// unchanged.
	Threshold int

	// PreviewLen is the leading-character count the stub carries. Zero
	// means DefaultOffloadPreview.
	PreviewLen int

	// PerToolThreshold overrides Threshold for named tools. A present
	// entry with value <= 0 pins that tool to never offload (always
	// inline), whatever its size — the escape hatch for a tool whose
	// full output the model must always see verbatim.
	PerToolThreshold map[string]int
}

// OffloadingSource wraps a ToolSource so that large successful tool
// results are stored out of band and replaced in the conversation by a
// compact stub, with a read_tool_result tool for fetching the detail on
// demand (the "just in time context" pattern). It composes exactly like
// FilterSource: put it around the aggregate MultiSource and hand the
// result to the Runner. No Runner change — the stub is a normal
// ToolResult, so the RoleTool message, the tool-end event, and the
// persisted log all carry the stub, keeping the log faithful to what the
// model actually saw.
//
// Only successful results are offloaded: IsError results stay inline
// (errors are usually short, and truncating one is worse than carrying
// it). The stored blob keeps the full result including StructuredContent;
// the stub is text-only.
type OffloadingSource struct {
	src   ToolSource
	store ToolResultStore
	cfg   OffloadConfig
	read  *FuncSource
}

// readToolResultArgs is the read_tool_result input. Offset/Limit take a
// character window over the flattened result; Pattern greps it line by
// line (a Go regexp) — querying *into* a huge output, which is where the
// win is, rather than paging the whole thing back.
type readToolResultArgs struct {
	Ref     string `json:"ref"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// NewOffloadingSource wraps src, offloading over-threshold results into
// store. Store is required; a nil store panics at construction rather
// than silently dropping results at call time.
func NewOffloadingSource(src ToolSource, store ToolResultStore, cfg OffloadConfig) *OffloadingSource {
	if store == nil {
		panic("agent: NewOffloadingSource requires a non-nil ToolResultStore")
	}
	if cfg.Threshold == 0 {
		cfg.Threshold = DefaultOffloadThreshold
	}
	if cfg.PreviewLen == 0 {
		cfg.PreviewLen = DefaultOffloadPreview
	}
	o := &OffloadingSource{src: src, store: store, cfg: cfg, read: NewFuncSource()}
	// Registration cannot fail for a fresh FuncSource with one unique
	// name; ignore the error to keep the constructor total.
	_ = AddFunc(o.read, ReadToolResultName,
		"Fetch a previously offloaded tool result by its ref. Give offset+limit for a character window, or pattern (a regular expression) to return only matching lines. Refs appear in tool-result stubs like [tool result 52KB, stored as res:ab12].",
		o.readToolResult)
	return o
}

// Tools lists the wrapped source's tools plus read_tool_result. The read
// tool is always offered while offloading is active, since a stub can
// appear on any call.
func (o *OffloadingSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	defs, err := o.src.Tools(ctx)
	if err != nil {
		return nil, err
	}
	readDefs, _ := o.read.Tools(ctx)
	return append(defs, readDefs...), nil
}

// Call routes read_tool_result to the offloader's own handler and every
// other name to the wrapped source, offloading the result when it is a
// successful over-threshold payload.
func (o *OffloadingSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name == ReadToolResultName {
		return o.read.Call(ctx, name, args)
	}
	res, err := o.src.Call(ctx, name, args)
	if err != nil || res == nil {
		return res, err
	}
	return o.maybeOffload(ctx, name, res)
}

// maybeOffload stores an over-threshold successful result and returns the
// stub; otherwise it returns the result unchanged.
func (o *OffloadingSource) maybeOffload(ctx context.Context, name string, res *core.ToolResult) (*core.ToolResult, error) {
	if res.IsError {
		return res, nil
	}
	threshold := o.cfg.Threshold
	if t, ok := o.cfg.PerToolThreshold[name]; ok {
		if t <= 0 {
			return res, nil // pinned inline
		}
		threshold = t
	}
	text := toolResultText(res)
	if len(text) < threshold {
		return res, nil
	}
	ref, err := newResultRef()
	if err != nil {
		// Minting failed (no entropy): fall back to inlining rather than
		// losing the result. Rare enough not to warrant a louder path.
		return res, nil
	}
	if _, err := o.store.PutToolResult(ctx, PutToolResultRequest{Ref: ref, Result: *res}); err != nil {
		return nil, fmt.Errorf("agent: offloading %q result: %w", name, err)
	}
	return &core.ToolResult{
		Content: []core.Content{{Type: "text", Text: o.stub(ref, text)}},
	}, nil
}

// stub renders the model-visible placeholder that replaces an offloaded
// result: size, ref, a leading preview, and the retrieval instruction.
func (o *OffloadingSource) stub(ref, text string) string {
	preview := text
	truncated := false
	if len(preview) > o.cfg.PreviewLen {
		preview = preview[:o.cfg.PreviewLen]
		truncated = true
	}
	ellipsis := ""
	if truncated {
		ellipsis = "…"
	}
	return fmt.Sprintf("[tool result %dB, stored as %s]\npreview: %s%s\ncall %s(ref=%q) for the full output — add offset+limit for a window or pattern to grep.",
		len(text), ref, preview, ellipsis, ReadToolResultName, ref)
}

// readToolResult serves a window or grep over an offloaded result. An
// unknown ref is a graceful (non-error) answer, since a stub can outlive
// its blob once a backend evicts it.
func (o *OffloadingSource) readToolResult(ctx context.Context, in readToolResultArgs) (string, error) {
	resp, err := o.store.GetToolResult(ctx, GetToolResultRequest{Ref: in.Ref})
	if err != nil {
		return "", fmt.Errorf("reading tool result %q: %w", in.Ref, err)
	}
	if !resp.Found {
		return fmt.Sprintf("tool result %q is no longer available (expired or evicted); re-run the tool if you still need it.", in.Ref), nil
	}
	text := toolResultText(&resp.Result)

	if in.Pattern != "" {
		re, err := regexp.Compile(in.Pattern)
		if err != nil {
			return "", fmt.Errorf("invalid pattern %q: %w", in.Pattern, err)
		}
		var matches []string
		for _, line := range strings.Split(text, "\n") {
			if re.MatchString(line) {
				matches = append(matches, line)
			}
		}
		if len(matches) == 0 {
			return fmt.Sprintf("no lines in %s match %q.", in.Ref, in.Pattern), nil
		}
		return strings.Join(matches, "\n"), nil
	}

	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(text) {
		return fmt.Sprintf("offset %d is past the end of %s (%d chars).", offset, in.Ref, len(text)), nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	window := text[offset:end]
	if end < len(text) {
		window += fmt.Sprintf("\n… (%d more chars; call again with offset=%d)", len(text)-end, end)
	}
	return window, nil
}

// newResultRef mints an opaque, collision-resistant ref for a stored
// result. 4 random bytes (8 hex chars) is ample within one store's
// lifetime; the store never relies on ref structure.
func newResultRef() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "res:" + hex.EncodeToString(b[:]), nil
}

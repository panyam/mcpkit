// Package checkpoint makes an agent's side effects undoable, by letting
// whoever wrote a tool say how to reverse it.
//
// The host cannot know how to undo create_issue, insert_row, or deploy, and
// there is no reason it should. Restricting "undo" to files would let the host
// own the mechanism, but only by assuming the answer. So reversal is a seam
// tools plug into, and the file snapshot in this package is its first
// implementation rather than its definition.
package checkpoint

import (
	"context"

	"github.com/panyam/mcpkit/agent"
)

// Reverser knows how to undo one tool's effects. It is supplied per tool by
// whoever knows what the tool does.
//
// Capture runs BEFORE the call, which is the only moment the pre-state still
// exists. A Reverser that needs nothing recorded returns the zero Reversal.
type Reverser interface {
	Capture(ctx context.Context, info agent.ToolCallInfo) (Reversal, error)
}

// Reversal is what a captured call offers for undoing itself, split into the
// part the harness may run unattended and the part it may not.
//
// The split is the whole design. Two operations get called "undo" and only one
// is safe to run automatically:
//
// Restore returns local state to what Capture saw. It is a genuine inverse —
// you land exactly where you were — and it is order-independent, idempotent,
// unaffected by what happened in between, and near-certain to succeed. Putting
// a file back is a restore.
//
// Compensate issues a NEW action that partially offsets an old one, like
// deleting an issue that was created. It is not an inverse: notifications
// fired, webhooks ran, and you end in a state where the issue existed and was
// deleted. It can fail on a permission the original call never needed, it is
// order-dependent, and it breaks outright once something has come to depend on
// the effect. So the harness surfaces compensations and lets a human decide,
// rather than chaining them automatically — which would be a saga
// orchestrator, and constraint A8 rules one out of this repo.
//
// A Reversal may carry both. A tool that writes a local cache and calls a
// remote API has a restore for one half and a compensation for the other.
type Reversal struct {
	// Restore returns local state to what Capture saw. Nil when the call
	// changed no local state.
	//
	// Must be idempotent and order-independent: the harness runs restores
	// unattended, in no guaranteed order, and may run the same one twice
	// after a partial failure.
	Restore func(ctx context.Context) error

	// Compensate names a call that partially offsets a remote effect. Nil
	// when there is nothing to offer.
	//
	// It is surfaced to the user and never invoked automatically. Populating
	// it is a statement that the call exists and is plausible, not a promise
	// that running it is correct or that it will succeed.
	Compensate *agent.ToolCall
}

// IsZero reports whether the call offered no way to reverse itself, which is
// what makes a tool irreversible in the sense ApprovalMode cares about.
func (r Reversal) IsZero() bool { return r.Restore == nil && r.Compensate == nil }

// Reversible reports whether this call can be undone automatically, meaning a
// Restore exists.
//
// A Compensate alone is deliberately NOT reversible: a compensating action is
// a new call with consequences of its own, so treating it as an undo would let
// a tool auto-approve on the strength of an offset it cannot guarantee. This
// is the property an approval policy should derive reversibility from, in
// preference to the destructiveHint annotation, which a server merely asserts.
func (r Reversal) Reversible() bool { return r.Restore != nil }

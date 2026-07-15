# agent/ Constraints

Module-specific rules. Project-wide constraints in the root `CONSTRAINTS.md` also apply (notably C1 typed contexts and C2 consolidated entry structs).

## A1: LLM-provider dependencies stay in agent/

No package outside `agent/` (root module, other sub-modules, examples that do not embed the agent) may import an LLM-provider SDK or this module. `agent/` depends downward on `core/`, `client/`, and optionally sibling sub-modules; nothing depends upward on it except applications and examples that embed the host.

**Verify:** `grep -rn "mcpkit/agent" core/ server/ client/ ext/ stores/ experimental/ --include='*.go'` returns nothing.

## A2: Runner events are wire-serializable

Every event type the Runner emits carries JSON tags, a stable `kind` discriminator, and no Go-only payloads (channels, funcs, non-marshalable interfaces). The wire projection used by web surfaces must be a 1:1 mapping, never a translation layer.

**Verify:** the event round-trip test in this module marshals and unmarshals every event kind through encoding/json and compares.

## A3: One vendor `_meta` prefix

All vendor-namespaced `_meta` keys this module reads or writes use `io.github.panyam.mcpkit/` (pinned in `docs/AGENT_DESIGN.md`). No ad-hoc prefixes.

**Verify:** `grep -rn '_meta\|Meta\[' agent/ --include='*.go' | grep -i 'io\.github\|dev\.\|com\.'` shows only the pinned prefix.

## A4: The loop never owns the user interface

The Runner exposes callbacks and event streams; it never prints, prompts, or renders. Anything user-facing lives in surfaces (agentchat, web hosts) built on the module.

**Verify:** `grep -rn "fmt.Print\|os.Stdout\|os.Stdin" agent/ --include='*.go' | grep -v _test.go` returns nothing.

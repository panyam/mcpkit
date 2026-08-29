# Extension Capability Declaration (SEP-2133)

How mcpkit advertises extensions in `capabilities.extensions`, and what changed in 0.6.

> **Status:** SEP-2133 is Final. mcpkit emitted a non-conformant shape until this change (issue 1334).

## TL;DR

| You are | Do |
|---|---|
| Writing an `ExtensionProvider` | Return `core.Extension{ID: ..., Settings: ...}`. Drop `SpecVersion` and `Stability`; they no longer exist. |
| Reading a server's extension settings | `cap, ok := client.ServerExtensionCapability(id)` then index directly: `cap["directoryRead"]`. There is no `.Config` to unwrap. |
| Declaring an extension with no settings | Leave `Settings` nil. It reaches the wire as `{}`. |

## What changed

SEP-2133 defines `extensions` as "a map of _extension identifiers_ to per-extension settings
objects. Each extension specifies the schema of its settings object; an empty object indicates no
settings."

mcpkit wrapped those settings in an envelope of its own, which put every setting one level below
where the spec puts it and added three fields the spec has nowhere to hold.

**Before:**

```json
"capabilities": {
  "extensions": {
    "io.modelcontextprotocol/skills": {
      "specVersion": "2026-04-23",
      "stability": "experimental",
      "config": { "directoryRead": true }
    },
    "io.mcpkit/auth": {
      "specVersion": "2025-11-25",
      "stability": "experimental"
    }
  }
}
```

**After:**

```json
"capabilities": {
  "extensions": {
    "io.modelcontextprotocol/skills": { "directoryRead": true },
    "io.mcpkit/auth": {}
  }
}
```

SEP-2640's own capability example matches the second form, so the two SEPs agree and mcpkit was the
outlier.

## Why the envelope was worse than a cosmetic mismatch

A client that follows the spec looks for `extensions[id].directoryRead`. Under the envelope that key
was at `extensions[id].config.directoryRead`, so a conformant client saw an extension declaring no
settings at all. mcpkit's `resources/directory/read` support was on, served, and invisible.

The conformance suite recorded this as six SKIPPED checks rather than a failure, because a server
that has not declared `directoryRead` is not required to serve the method. A suite reporting 1/1
passed was testing nothing. After the fix the same suite runs 7/7.

## Code changes

### Declaring an extension

```go
// Before
func (e SkillsExtension) Extension() core.Extension {
	ext := core.Extension{
		ID:          ExtensionID,
		SpecVersion: SpecVersion,
		Stability:   core.Experimental,
	}
	if e.DirectoryRead {
		ext.Config = map[string]any{CapabilityDirectoryRead: true}
	}
	return ext
}

// After
func (e SkillsExtension) Extension() core.Extension {
	ext := core.Extension{ID: ExtensionID}
	if e.DirectoryRead {
		ext.Settings = map[string]any{CapabilityDirectoryRead: true}
	}
	return ext
}
```

### Reading settings on the client

```go
// Before
cap, ok := c.mcp.ServerExtensionCapability(ExtensionID)
v, _ := cap.Config[CapabilityDirectoryRead].(bool)

// After
cap, ok := c.mcp.ServerExtensionCapability(ExtensionID)
v, _ := cap[CapabilityDirectoryRead].(bool)
```

`core.ExtensionCapability` is now `map[string]any`, matching the untyped settings object the spec
describes. Each extension owns the schema under its own reverse-domain identifier, so callers should
read unknown keys permissively.

## Removed API

| Symbol | Replacement |
|---|---|
| `core.Extension.SpecVersion` | A package-level constant in the extension's own package, e.g. `skills.SpecVersion`. Not carried on the wire. |
| `core.Extension.Stability` | None. SEP-2133 has no slot for it and nothing read it back. |
| `core.Extension.Config` | `core.Extension.Settings` |
| `core.Stability`, `core.Experimental`, `core.Stable`, `core.Deprecated` | None. |
| `core.ExtensionCapability.{SpecVersion,Stability,Config}` | Index the capability directly. |

Nothing in the tree read `specVersion` or `stability` off the wire, so no consumer behaviour depended
on them. They were write-only metadata for their whole life.

## The empty-object rule

SEP-2133 gives `{}` the specific meaning "supports the extension, no optional features". A nil Go map
marshals to `null`, which the spec does not define, so `ExtensionCapability` carries a `MarshalJSON`
that renders nil as `{}`. This matters more than it sounds: five of mcpkit's six extensions declare
no settings, so the empty object is the common path rather than an edge case. The conformance check
is `sep-2640-capability-empty-object`.

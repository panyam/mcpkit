#!/usr/bin/env python3
"""Wire-surface parity diff between a mcpkit-Go compat fixture and the
upstream TypeScript reference server.

Connects to two MCP servers via Streamable HTTP (no SDK — direct
JSON-RPC over POST + SSE), fetches and diffs five surfaces:

  1. initialize — serverInfo (name, version) + capabilities. Protocol-
     negotiation drift (e.g. mcpkit claiming a capability upstream
     doesn't) surfaces here.

  2. tools/list — each tool's name + title + description + input/
     output schema + tool-level `_meta`. The original surface the
     diff covered.

  3. resources/list — what resources the server advertises (URI, name,
     mimeType, description, resource-def `_meta`). Catches a fixture that
     registered a resource on the wrong URI.

  4. resources/templates/list — URI templates (URITemplate registrations).
     Catches drift in parameterized resource patterns (pdf-server's
     pending interact surface, future template-based fixtures).

  5. resources/read — for each unique `_meta.ui.resourceUri` referenced
     by a tool, fetch the resource and compare the per-content `_meta`.
     This is where the spec actually puts `permissions` and `csp` (see
     McpUiResourceMeta in the upstream type defs); the tools/list pass
     cannot see those.

Why surfaces 1-5 matter: the previous tools-only diff missed the bugs
fixed in PR 623 (transcript missing `_meta.ui.permissions`) and PR 643
(sheet-music missing `_meta.ui.csp.connectDomains`) because both bugs
live on the resource read response, not on tool_meta.

LEGACY WIRE ONLY. This script speaks the Streamable HTTP wire with an
explicit `initialize` round-trip + Mcp-Session-Id; that's what every
apps/compat fixture uses today. SEP-2575's stateless wire has no
`initialize` step (per-request `_meta` carries protocolVersion +
capabilities), so a stateless parity audit needs a parallel code path.
None of the compat fixtures use stateless today; if/when one does, the
audit will need extension.

Used standalone (this script) for ad-hoc inspection AND in CI via the
DOCKER-mode Playwright wrapper (scripts/apps-playwright-docker-inner.sh
copies this in and invokes it after the fixture + upstream pair come up).

Usage:
  uv run scripts/apps_parity_diff.py <a-label> <a-url> <b-label> <b-url>

Exit codes:
  0 — parity OK across all 5 surfaces
  1 — drift detected; per-surface unified diff printed to stderr
  2 — usage error

Replaces scripts/apps-playwright-tools-diff.mjs (Node ESM, only covered
tools/list). Stdlib only.
"""
from __future__ import annotations

import difflib
import json
import sys
import urllib.error
import urllib.request
import uuid
from typing import Any


# Keys stripped before comparison — SDK-level emit differences that don't
# change semantics on the wire:
#
#   $schema             — mcpkit emits draft-2020-12 via invopop, upstream's
#                         TS SDK emits draft-07 via zod-to-json-schema.
#                         Presence matters for clients; value is the SDK's
#                         own draft choice.
#
#   additionalProperties — mcpkit's invopop omits (permissive default);
#                          upstream's zod-to-json-schema emits `false`
#                          (strict). Documented in core/schema.go.
#
#   propertyNames        — upstream's zod `z.record(z.string(), z.unknown())`
#                          emits `{"propertyNames": {"type": "string"}}` for
#                          string-keyed maps; mcpkit's `map[string]any`
#                          omits it. Both mean the same thing.
IGNORE_KEYS = {"$schema", "additionalProperties", "propertyNames"}


# Resource fields that vary by server build artifact and should not gate
# parity. `text` is the iframe HTML body — different bundlers will produce
# different bytes (whitespace, module ordering); a fixture that mirrors
# upstream verbatim still won't byte-match if the diff comparator is
# strict. `blob` similarly. The interesting parity surface is `_meta`
# (sandbox policy declarations), `mimeType`, and `uri` — those stay in.
RESOURCE_BODY_KEYS_IGNORED = {"text", "blob"}


def _post_json_rpc(
    url: str, body: dict, headers: dict[str, str] | None = None
) -> tuple[dict[str, str], dict]:
    """POST a JSON-RPC envelope. Returns (response_headers, parsed_body).

    Streamable HTTP returns either application/json or text/event-stream.
    We accept both; if it's SSE, we read the first `data:` frame's JSON
    payload (sufficient for all our use cases — initialize / tools/list /
    resources/read are single-response calls).
    """
    req_headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(
        url, data=json.dumps(body).encode("utf-8"), headers=req_headers, method="POST"
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        resp_headers = {k.lower(): v for k, v in resp.headers.items()}
        ctype = resp_headers.get("content-type", "")
        raw = resp.read().decode("utf-8")
    # Notifications (notifications/initialized, etc.) have no response
    # body — JSON-RPC one-way calls. Server returns HTTP 202 Accepted with
    # an empty body. Return empty parsed body in that case.
    if not raw.strip():
        return resp_headers, {}
    if "event-stream" in ctype:
        for line in raw.splitlines():
            if line.startswith("data:"):
                return resp_headers, json.loads(line[5:].strip())
        raise RuntimeError(f"SSE response with no data frame: {raw[:200]}")
    return resp_headers, json.loads(raw)


def _initialize(url: str) -> tuple[str, dict]:
    """Run initialize + notifications/initialized.

    Returns (session_id, initialize_result). The initialize_result is
    the full server response, used by the parity pass to compare
    serverInfo + capabilities between mcpkit and upstream.

    Streamable HTTP can run in two modes: session-based (server returns
    `Mcp-Session-Id` in the initialize response, clients echo it back on
    subsequent requests) or session-less (no session header; each
    request stands alone). mcpkit defaults to session-based; upstream's
    TS reference server defaults to session-less. Both are spec-compliant.
    `sid` is "" in session-less mode; `_call` reads it lazily and only
    adds the header when non-empty.
    """
    init_body = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize",
        "params": {
            "protocolVersion": "2025-11-25",
            "capabilities": {},
            "clientInfo": {"name": "parity-diff", "version": "1.0"},
        },
    }
    headers, resp = _post_json_rpc(url, init_body)
    sid = headers.get("mcp-session-id", "")
    notif_headers = {"Mcp-Session-Id": sid} if sid else None
    _post_json_rpc(
        url,
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        notif_headers,
    )
    return sid, resp.get("result", {})


def _call(url: str, sid: str, method: str, params: dict | None = None) -> dict:
    """Single JSON-RPC call against an initialized server. Returns `result`.

    Sends Mcp-Session-Id only when sid is non-empty (mcpkit's session-based
    default); session-less servers (upstream's TS default) get no header.
    """
    body = {
        "jsonrpc": "2.0",
        "id": str(uuid.uuid4()),
        "method": method,
        "params": params or {},
    }
    headers = {"Mcp-Session-Id": sid} if sid else None
    _, resp = _post_json_rpc(url, body, headers)
    if "error" in resp:
        raise RuntimeError(f"{method} returned error: {resp['error']}")
    return resp.get("result", {})


def _normalize_type_value(v: Any) -> Any:
    """JSON Schema 'integer' validates against 'number' too. mcpkit's
    invopop reflects Go `int` → 'integer'; upstream's zod-to-json-schema
    always emits 'number' (TS `number` is a 64-bit float). Both descriptions
    are valid for integer values; collapse to 'number' so the SDK
    divergence doesn't manifest as drift."""
    return "number" if v == "integer" else v


def _deep_sort_keys(value: Any, parent_key: str | None = None) -> Any:
    """Canonicalize a parsed-JSON value: sort keys, strip IGNORE_KEYS,
    normalize type. Mirrors the .mjs predecessor's deepSortKeys."""
    if isinstance(value, list):
        return [_deep_sort_keys(v) for v in value]
    if isinstance(value, dict):
        out = {}
        for k in sorted(value.keys()):
            if k in IGNORE_KEYS:
                continue
            out[k] = _deep_sort_keys(value[k], k)
        return out
    if parent_key == "type":
        return _normalize_type_value(value)
    return value


def _normalize_tools(tools: list[dict]) -> list[dict]:
    return [
        _deep_sort_keys(t)
        for t in sorted(tools, key=lambda t: t.get("name", ""))
    ]


def _normalize_resource_contents(contents: list[dict]) -> list[dict]:
    """Strip the iframe-body fields (text/blob) and canonicalize what
    remains. The parity surface for resources is _meta, mimeType, uri —
    NOT the bundled HTML body (different bundlers produce different bytes)."""
    out = []
    for c in contents:
        c2 = {k: v for k, v in c.items() if k not in RESOURCE_BODY_KEYS_IGNORED}
        out.append(_deep_sort_keys(c2))
    return out


def _collect_resource_uris(tools: list[dict]) -> list[str]:
    """Walk tools/list and extract every unique `_meta.ui.resourceUri`
    referenced by a tool's `_meta.ui`. mcpkit also emits the flat
    `ui/resourceUri` form for back-compat; both yield the same URI.

    Template URIs (containing `{var}`) are skipped — they require
    parameter binding to read, which is beyond the scope of a static
    parity diff. Concrete URIs only.
    """
    uris = []
    seen = set()
    for tool in tools:
        meta = tool.get("_meta") or {}
        ui = meta.get("ui") or {}
        uri = ui.get("resourceUri") or meta.get("ui/resourceUri")
        if not uri or uri in seen:
            continue
        if "{" in uri:
            continue
        seen.add(uri)
        uris.append(uri)
    return uris


def _unified_diff(a: str, b: str, a_label: str, b_label: str) -> str:
    return "\n".join(
        difflib.unified_diff(
            a.splitlines(),
            b.splitlines(),
            fromfile=a_label,
            tofile=b_label,
            lineterm="",
        )
    )


def _diff_initialize(
    a_init: dict, b_init: dict, a_label: str, b_label: str
) -> tuple[bool, str]:
    """Compare initialize results — serverInfo + fixture-relevant capabilities.

    mcpkit's server framework auto-advertises a wider capability set than
    upstream's per-example servers (mcpkit ships with completions, logging,
    and the extensions registry hooked up; upstream's basic-server-vanillajs
    advertises only what its fixture-level code claims). Naively comparing
    `capabilities` would mark every fixture as drift on these framework
    differences.

    Project both sides to the capabilities that are actually fixture-
    relevant (the ones a fixture either implements or not):
      - tools, resources, prompts: data the fixture registers
      - sampling, elicitation: fixture-side opt-in (sample/elicit calls)
      - roots: client-side, but conventionally surfaces here

    Skip:
      - completions, logging: mcpkit-framework-wide, fixture-agnostic
      - extensions: mcpkit auto-registers the UI extension on every
        compat fixture by virtue of using ui.RegisterTypedAppTool;
        comparing surfaces a framework-vs-non-framework divergence
        rather than a per-fixture drift
    """
    FIXTURE_RELEVANT_CAPS = {
        "tools", "resources", "prompts",
        "sampling", "elicitation", "roots",
    }

    def _project(init: dict) -> dict:
        out = {}
        if "serverInfo" in init:
            out["serverInfo"] = init["serverInfo"]
        caps = init.get("capabilities") or {}
        out["capabilities"] = {
            k: v for k, v in caps.items() if k in FIXTURE_RELEVANT_CAPS
        }
        return _deep_sort_keys(out)

    a_norm = _project(a_init)
    b_norm = _project(b_init)
    a_json = json.dumps(a_norm, indent=2, sort_keys=False)
    b_json = json.dumps(b_norm, indent=2, sort_keys=False)
    if a_json == b_json:
        return True, ""
    return False, _unified_diff(a_json, b_json, a_label, b_label)


def _diff_tools(
    a_url: str,
    b_url: str,
    a_label: str,
    b_label: str,
    a_sid: str,
    b_sid: str,
) -> tuple[bool, str, list[dict], list[dict]]:
    """Returns (parity_ok, diff_text, a_tools, b_tools).

    The raw tool lists are returned so the resource pass can read each
    side's _meta.ui.resourceUri without a second round-trip.
    """
    a_tools = _call(a_url, a_sid, "tools/list").get("tools", [])
    b_tools = _call(b_url, b_sid, "tools/list").get("tools", [])
    a_norm = _normalize_tools(a_tools)
    b_norm = _normalize_tools(b_tools)
    a_json = json.dumps(a_norm, indent=2, sort_keys=False)
    b_json = json.dumps(b_norm, indent=2, sort_keys=False)
    if a_json == b_json:
        return True, "", a_tools, b_tools
    return False, _unified_diff(a_json, b_json, a_label, b_label), a_tools, b_tools


def _diff_resources_list(
    a_url: str, b_url: str, a_label: str, b_label: str, a_sid: str, b_sid: str
) -> tuple[bool, str]:
    """Compare resources/list — URI, name, mimeType, description, resource-
    def _meta. Catches a fixture that registered on the wrong URI or with
    drifted metadata."""
    try:
        a_resources = _call(a_url, a_sid, "resources/list").get("resources", [])
    except RuntimeError as exc:
        # Some servers may not advertise resources/list at all (no resources
        # registered). Treat as empty rather than failure.
        if "Method not found" in str(exc):
            a_resources = []
        else:
            raise
    try:
        b_resources = _call(b_url, b_sid, "resources/list").get("resources", [])
    except RuntimeError as exc:
        if "Method not found" in str(exc):
            b_resources = []
        else:
            raise
    a_norm = [
        _deep_sort_keys(r) for r in sorted(a_resources, key=lambda r: r.get("uri", ""))
    ]
    b_norm = [
        _deep_sort_keys(r) for r in sorted(b_resources, key=lambda r: r.get("uri", ""))
    ]
    a_json = json.dumps(a_norm, indent=2, sort_keys=False)
    b_json = json.dumps(b_norm, indent=2, sort_keys=False)
    if a_json == b_json:
        return True, ""
    return False, _unified_diff(a_json, b_json, a_label, b_label)


def _diff_resource_templates(
    a_url: str, b_url: str, a_label: str, b_label: str, a_sid: str, b_sid: str
) -> tuple[bool, str]:
    """Compare resources/templates/list — URI templates with placeholders.
    Catches drift in parameterized resource patterns."""
    try:
        a_templates = _call(a_url, a_sid, "resources/templates/list").get(
            "resourceTemplates", []
        )
    except RuntimeError as exc:
        if "Method not found" in str(exc):
            a_templates = []
        else:
            raise
    try:
        b_templates = _call(b_url, b_sid, "resources/templates/list").get(
            "resourceTemplates", []
        )
    except RuntimeError as exc:
        if "Method not found" in str(exc):
            b_templates = []
        else:
            raise
    a_norm = [
        _deep_sort_keys(t)
        for t in sorted(a_templates, key=lambda t: t.get("uriTemplate", ""))
    ]
    b_norm = [
        _deep_sort_keys(t)
        for t in sorted(b_templates, key=lambda t: t.get("uriTemplate", ""))
    ]
    a_json = json.dumps(a_norm, indent=2, sort_keys=False)
    b_json = json.dumps(b_norm, indent=2, sort_keys=False)
    if a_json == b_json:
        return True, ""
    return False, _unified_diff(a_json, b_json, a_label, b_label)


def _diff_resources(
    a_url: str,
    b_url: str,
    a_label: str,
    b_label: str,
    a_sid: str,
    b_sid: str,
    uris: list[str],
) -> tuple[bool, str]:
    """For each URI, fetch resources/read from both servers and compare
    the canonicalized `contents[]._meta + mimeType + uri`. The iframe
    HTML body is intentionally not compared.
    """
    if not uris:
        return True, ""

    per_uri_diffs = []
    for uri in uris:
        try:
            a_res = _call(a_url, a_sid, "resources/read", {"uri": uri})
        except Exception as exc:
            per_uri_diffs.append(f"{uri}: {a_label} failed: {exc}")
            continue
        try:
            b_res = _call(b_url, b_sid, "resources/read", {"uri": uri})
        except Exception as exc:
            per_uri_diffs.append(f"{uri}: {b_label} failed: {exc}")
            continue
        a_norm = _normalize_resource_contents(a_res.get("contents") or [])
        b_norm = _normalize_resource_contents(b_res.get("contents") or [])
        a_json = json.dumps(a_norm, indent=2, sort_keys=False)
        b_json = json.dumps(b_norm, indent=2, sort_keys=False)
        if a_json == b_json:
            continue
        per_uri_diffs.append(
            f"DRIFT on {uri}:\n"
            + _unified_diff(a_json, b_json, f"{a_label} {uri}", f"{b_label} {uri}")
        )

    if not per_uri_diffs:
        return True, ""
    return False, "\n\n".join(per_uri_diffs)


def main(argv: list[str]) -> int:
    if len(argv) != 5:
        print(
            "Usage: apps_parity_diff.py <a-label> <a-url> <b-label> <b-url>",
            file=sys.stderr,
        )
        return 2
    a_label, a_url, b_label, b_url = argv[1:]

    # Initialize each server once and reuse the session across all 5 surfaces.
    a_sid, a_init = _initialize(a_url)
    b_sid, b_init = _initialize(b_url)

    init_ok, init_diff = _diff_initialize(a_init, b_init, a_label, b_label)
    tools_ok, tools_diff, a_tools, b_tools = _diff_tools(
        a_url, b_url, a_label, b_label, a_sid, b_sid
    )
    rlist_ok, rlist_diff = _diff_resources_list(
        a_url, b_url, a_label, b_label, a_sid, b_sid
    )
    rtmpl_ok, rtmpl_diff = _diff_resource_templates(
        a_url, b_url, a_label, b_label, a_sid, b_sid
    )

    # Union of both sides' resourceUris is the audit surface — a fixture
    # that omits a tool would otherwise hide its resource from the diff.
    uris = sorted(
        {u for u in _collect_resource_uris(a_tools) + _collect_resource_uris(b_tools)}
    )
    rread_ok, rread_diff = _diff_resources(
        a_url, b_url, a_label, b_label, a_sid, b_sid, uris
    )

    all_ok = init_ok and tools_ok and rlist_ok and rtmpl_ok and rread_ok
    if all_ok:
        print(
            f"parity OK between {a_label} and {b_label} "
            f"({len(a_tools)} tools, {len(uris)} ui:// resources)"
        )
        return 0

    for label, ok, diff in (
        ("initialize (serverInfo + capabilities)", init_ok, init_diff),
        ("tools/list", tools_ok, tools_diff),
        ("resources/list", rlist_ok, rlist_diff),
        ("resources/templates/list", rtmpl_ok, rtmpl_diff),
        ("resources/read _meta", rread_ok, rread_diff),
    ):
        if ok:
            continue
        print(
            f"DRIFT in {label} between {a_label} ({a_url}) and {b_label} ({b_url})",
            file=sys.stderr,
        )
        print("", file=sys.stderr)
        print(diff, file=sys.stderr)
        print("", file=sys.stderr)

    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))

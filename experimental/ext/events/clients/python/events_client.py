#!/usr/bin/env python3
"""
Generic MCP Events client — works with any MCP server that uses the events extension.

Subcommands:
    list      Show server capabilities: tools, resources, events, sample poll
    listen    Open SSE stream, print push events in real time
    webhook   Start local HTTP receiver, subscribe via events/subscribe
    poll      Polling loop: call events/poll on an interval

Usage:
    python3 events_client.py list    --event discord.message --resource-uri discord://messages/recent
    python3 events_client.py listen  --event discord.message
    python3 events_client.py webhook --event discord.message --port 9999
    python3 events_client.py poll    --event discord.message --interval 5

Common flags:
    --mcp URL       MCP server endpoint (default: http://localhost:8080/mcp)
    --event NAME    Event name for poll/subscribe (default: required for poll/webhook)
"""

import argparse
import base64
import hashlib
import hmac
import json
import os
import sys
import textwrap
import threading
import time
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def generate_webhook_secret() -> str:
    """Returns a Standard-Webhooks-shaped client-supplied secret:
    `whsec_` + base64 of 32 random bytes (256 bits, well inside the
    spec-mandated 24-64 byte range). Mirrors events.GenerateSecret() in
    the Go SDK so both SDKs produce the same shape."""
    return "whsec_" + base64.urlsafe_b64encode(os.urandom(32)).rstrip(b"=").decode("ascii")


# ═══════════════════════════════════════════════════════════════════
# MCP session helpers
# ═══════════════════════════════════════════════════════════════════

def _fmt_cursor(c) -> str:
    """Render a cursor for human-readable logging. Cursored sources emit a
    string; cursorless sources emit JSON null which deserializes to Python
    None — render that as a visible token rather than the literal "None"."""
    if c is None:
        return "(none)"
    return c


class MCPSession:
    """Thin wrapper around an MCP Streamable HTTP session."""

    def __init__(self, mcp_url: str):
        self.url = mcp_url
        self.sid = None
        self._next_id = 0

    def _id(self) -> int:
        self._next_id += 1
        return self._next_id

    def initialize(self):
        """Send initialize, capture Mcp-Session-Id."""
        body = json.dumps({
            "jsonrpc": "2.0", "id": self._id(),
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "clientInfo": {"name": "events-client", "version": "1.0"},
                "capabilities": {},
            },
        }).encode()

        req = Request(self.url, data=body, headers={"Content-Type": "application/json"})
        with urlopen(req) as resp:
            for k, v in resp.headers.items():
                if k.lower() == "mcp-session-id":
                    self.sid = v.strip()
                    break
        if not self.sid:
            sys.exit("ERROR: no Mcp-Session-Id header — is the server running?")

        # Send notifications/initialized
        self._notify("notifications/initialized")
        return self.sid

    def _notify(self, method: str, params=None):
        msg = {"jsonrpc": "2.0", "method": method}
        if params:
            msg["params"] = params
        req = Request(self.url, data=json.dumps(msg).encode(), headers={
            "Content-Type": "application/json",
            "Mcp-Session-Id": self.sid,
        })
        with urlopen(req) as resp:
            resp.read()

    def rpc(self, method: str, params=None) -> dict:
        """Send a JSON-RPC request, parse the SSE data: line response."""
        msg = {"jsonrpc": "2.0", "id": self._id(), "method": method}
        if params is not None:
            msg["params"] = params
        req = Request(self.url, data=json.dumps(msg).encode(), headers={
            "Content-Type": "application/json",
            "Mcp-Session-Id": self.sid,
            "Accept": "text/event-stream",
        })
        with urlopen(req) as resp:
            raw = resp.read().decode()

        # Response is SSE: lines starting with "data: "
        for line in raw.splitlines():
            if line.startswith("data: "):
                return json.loads(line[6:])
        return {}

    def open_sse_stream(self, callback):
        """GET the MCP endpoint to open a long-lived SSE stream.
        Calls callback(parsed_json) for each data: line."""
        import urllib.request
        req = urllib.request.Request(self.url, headers={
            "Mcp-Session-Id": self.sid,
            "Accept": "text/event-stream",
        })
        resp = urllib.request.urlopen(req)
        buf = b""
        while True:
            chunk = resp.read(1)
            if not chunk:
                break
            buf += chunk
            if buf.endswith(b"\n"):
                line = buf.decode().rstrip("\r\n")
                buf = b""
                if line.startswith("data: "):
                    try:
                        callback(json.loads(line[6:]))
                    except json.JSONDecodeError:
                        pass


# ═══════════════════════════════════════════════════════════════════
# Subcommand: list
# ═══════════════════════════════════════════════════════════════════

def cmd_list(session: MCPSession, args):
    print("=== MCP Events — Server Capabilities ===\n")

    sid = session.initialize()
    print(f"Session: {sid}\n")

    # tools/list
    print("--- tools/list ---")
    resp = session.rpc("tools/list", {})
    for t in resp.get("result", {}).get("tools", []):
        print(f"  {t['name']}")
    print()

    # resources/list
    print("--- resources/list ---")
    resp = session.rpc("resources/list", {})
    for r in resp.get("result", {}).get("resources", []):
        print(f"  {r['uri']}")
    print()

    # events/list
    print("--- events/list ---")
    resp = session.rpc("events/list", {})
    for ev in resp.get("result", {}).get("events", []):
        print(json.dumps(ev, indent=2))
    print()

    # events/poll
    if args.event:
        print(f"--- events/poll (cursor=0, event={args.event}) ---")
        resp = session.rpc("events/poll", {
            "subscriptions": [{"id": "list", "name": args.event, "cursor": "0"}],
        })
        result = resp.get("result", {})
        events = result.get("events", [])
        n = len(events)
        print(f"  {n} event(s), cursor={_fmt_cursor(result.get('cursor'))}, hasMore={result.get('hasMore')}")
        for ev in events[:3]:
            print(f"    {ev.get('eventId')}: {json.dumps(ev.get('data', {}), separators=(',', ':'))[:120]}")
        print()

    # resource read
    if args.resource_uri:
        print(f"--- {args.resource_uri} ---")
        resp = session.rpc("resources/read", {"uri": args.resource_uri})
        contents = resp.get("result", {}).get("contents", [])
        if contents:
            text = contents[0].get("text", "")
            try:
                items = json.loads(text)
                print(f"  {len(items)} item(s) in store")
                for item in items[-3:]:
                    sender = item.get("sender", item.get("author", {}).get("username", "?"))
                    print(f"    {sender}: {item.get('text', item.get('content', ''))}")
            except (json.JSONDecodeError, TypeError):
                print(f"  {text[:200]}")
        print()

    print("=== Done ===")


# ═══════════════════════════════════════════════════════════════════
# Subcommand: listen (SSE push)
# ═══════════════════════════════════════════════════════════════════

def cmd_listen(session: MCPSession, args):
    print("=== MCP Events — SSE Listener ===\n")

    sid = session.initialize()
    print(f"Session: {sid}")
    print("Listening for events (Ctrl-C to stop)...")
    print('  Inject in another terminal: make inject TEXT="hello"\n')

    def on_message(msg):
        method = msg.get("method", "")
        if method == "notifications/events/event":
            p = msg.get("params", {})
            print("── EVENT " + "─" * 45)
            print(f"  id:      {p.get('eventId', '')}")
            print(f"  name:    {p.get('name', '')}")
            print(f"  time:    {p.get('timestamp', '')}")
            print(f"  cursor:  {_fmt_cursor(p.get('cursor'))}")
            data = p.get("data")
            if data:
                print(json.dumps(data, indent=2))
            print()
            sys.stdout.flush()
        elif method:
            print(f"[notification] {method}")
            sys.stdout.flush()

    try:
        session.open_sse_stream(on_message)
    except KeyboardInterrupt:
        print("\nStopped.")


# ═══════════════════════════════════════════════════════════════════
# WebhookSubscription — TTL refresh + recovery helper
# ═══════════════════════════════════════════════════════════════════

class WebhookSubscription:
    """Wraps an events/subscribe lifecycle with automatic TTL refresh.

    Per the spec, webhook subscriptions are soft state: the server holds them
    in memory and they expire if the client stops refreshing. Per Peter's
    response on the WG PR (line 623), it's allowed to deliver near the TTL
    boundary and the client should expect a "subscription not found" error
    when refreshing — at which point the right move is to re-subscribe.

    This helper does both:
      - Schedules a refresh at refresh_factor * TTL (default 0.5).
      - On any refresh that fails (subscription expired, server restarted),
        immediately re-subscribes with the same parameters.

    Use start() / stop() to manage the background refresh thread.
    """

    def __init__(
        self,
        session,
        event_name: str,
        callback_url: str,
        secret: str = "",
        sub_id: str = "wh-sub",
        refresh_factor: float = 0.5,
        on_event: callable = None,
        on_refresh: callable = None,
        on_recover: callable = None,
    ):
        """
        secret: client-supplied HMAC signing secret. Per spec, must be
                whsec_ + base64 of 24-64 random bytes. If empty, the SDK
                auto-generates a spec-conformant value via
                generate_webhook_secret().
        on_refresh: called after each successful refresh (including the initial
                    subscribe and any post-recovery re-subscribe). Receives no args.
        on_recover: called when a refresh fails (subscription expired) and a
                    fresh subscribe succeeds. Receives no args. Both may fire
                    in the same cycle if the recovery succeeds.
        """
        self.session = session
        self.event_name = event_name
        self.callback_url = callback_url
        self.secret = secret if secret else generate_webhook_secret()
        self.sub_id = sub_id
        self.refresh_factor = refresh_factor
        self.on_event = on_event
        self.on_refresh = on_refresh
        self.on_recover = on_recover

        self._stop = threading.Event()
        self._thread = None
        self._refresh_before = None  # parsed from server response

    def _subscribe(self):
        """Send events/subscribe and capture refreshBefore."""
        resp = self.session.rpc("events/subscribe", {
            "id": self.sub_id,
            "name": self.event_name,
            "delivery": {
                "mode": "webhook",
                "url": self.callback_url,
                "secret": self.secret,
            },
        })
        result = resp.get("result", {})
        rb_str = result.get("refreshBefore")
        if rb_str:
            # RFC3339 — strip trailing Z and parse as UTC
            self._refresh_before = datetime.fromisoformat(rb_str.replace("Z", "+00:00"))
        return result

    def _seconds_until_refresh(self) -> float:
        """How long to wait before the next refresh."""
        if self._refresh_before is None:
            return 30.0  # conservative fallback
        ttl_remaining = (self._refresh_before - datetime.now(timezone.utc)).total_seconds()
        # refresh_factor * TTL_remaining; clamp to [1.0, ttl_remaining]
        wait = max(1.0, ttl_remaining * self.refresh_factor)
        # Don't sleep past the boundary either
        return min(wait, max(1.0, ttl_remaining - 1.0))

    def _refresh_loop(self):
        """Background loop: sleep, refresh, repeat. On any refresh failure,
        treat as expired and re-subscribe immediately."""
        while not self._stop.is_set():
            wait = self._seconds_until_refresh()
            # Use Event.wait so stop() interrupts the sleep
            if self._stop.wait(timeout=wait):
                return
            try:
                self._subscribe()
                if self.on_refresh:
                    self.on_refresh()
            except (HTTPError, OSError, ValueError) as exc:
                # Subscription was likely expired (race near TTL boundary,
                # per Peter's WG comment). Re-subscribe and notify.
                print(f"  [refresh] {exc.__class__.__name__}: {exc} — re-subscribing", flush=True)
                try:
                    self._subscribe()
                    if self.on_refresh:
                        self.on_refresh()
                    if self.on_recover:
                        self.on_recover()
                except Exception as exc2:
                    print(f"  [refresh] re-subscribe failed: {exc2}", flush=True)

    def start(self):
        """Subscribe and start the background refresh thread. on_refresh fires
        once for the initial subscribe so callers that count refresh calls see
        a consistent total."""
        result = self._subscribe()
        if self.on_refresh:
            self.on_refresh()
        self._thread = threading.Thread(target=self._refresh_loop, daemon=True)
        self._thread.start()
        return result

    def stop(self):
        """Stop the refresh thread. Does not unsubscribe."""
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=2.0)


# ═══════════════════════════════════════════════════════════════════
# Subcommand: webhook
# ═══════════════════════════════════════════════════════════════════

def _verify_signature(headers, body: bytes, secret: str) -> bool:
    """Verify either X-MCP-* or webhook-* signature on an inbound delivery.
    Detects which header set is present rather than requiring the caller to
    know the server's mode — handy for ad-hoc receivers and tests.
    """
    mcp_sig = headers.get("X-MCP-Signature", "")
    if mcp_sig:
        ts = headers.get("X-MCP-Timestamp", "")
        expected = "sha256=" + hmac.new(
            secret.encode(), (ts + ".").encode() + body, hashlib.sha256
        ).hexdigest()
        return hmac.compare_digest(mcp_sig, expected)

    std_sig = headers.get("webhook-signature", "")
    if std_sig:
        msg_id = headers.get("webhook-id", "")
        ts = headers.get("webhook-timestamp", "")
        import base64
        expected = "v1," + base64.b64encode(
            hmac.new(secret.encode(), (msg_id + "." + ts + ".").encode() + body, hashlib.sha256).digest()
        ).decode()
        # Standard Webhooks allows multiple space-separated v1 sigs; any match wins.
        for cand in std_sig.split():
            if hmac.compare_digest(cand, expected):
                return True
        return False
    return False


def _make_webhook_handler(secret_holder):
    """secret_holder is a list with one element — `secret_holder[0]` —
    even though the secret no longer changes mid-flight under the spec
    (client-supplied only, no server-side rotation). The list-of-one
    indirection is kept so the handler closure doesn't need a mutex on
    the rare case of a future secret rotation feature."""
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)
            sig_ok = _verify_signature(self.headers, body, secret_holder[0])
            mode = "standard" if self.headers.get("webhook-signature") else "mcp"

            label = f"{mode} sig OK" if sig_ok else f"{mode} sig FAIL"
            print()
            print(f"── WEBHOOK EVENT ({label}) " + "─" * 30)
            try:
                event = json.loads(body)
                print(f"  id:      {event.get('eventId', '')}")
                print(f"  name:    {event.get('name', '')}")
                print(f"  time:    {event.get('timestamp', '')}")
                print(f"  cursor:  {_fmt_cursor(event.get('cursor'))}")
                data = event.get("data")
                if data:
                    print(json.dumps(data, indent=2))
            except Exception:
                print(f"  raw: {body.decode()}")
            print()
            sys.stdout.flush()
            self.send_response(200)
            self.end_headers()

        def log_message(self, *_args):
            pass

    return Handler


def cmd_webhook(session: MCPSession, args):
    if not args.event:
        sys.exit("ERROR: --event is required for webhook mode")

    hook_url = f"http://localhost:{args.port}"

    print("=== MCP Events — Webhook Receiver ===\n")

    # Subscription owns the secret (auto-generated if --secret omitted,
    # else uses the supplied value). Pass it to the receiver via a
    # single-element list so the closure reads the live value (allows
    # for future rotation features without mutex juggling).
    sub = WebhookSubscription(
        session,
        event_name=args.event,
        callback_url=hook_url,
        secret=args.secret,  # empty → SDK auto-generates a whsec_ value
        sub_id="wh-demo",
        refresh_factor=args.refresh_factor,
    )
    secret_holder = [sub.secret]

    print(f"Starting webhook receiver on port {args.port}...")
    server = HTTPServer(("", args.port), _make_webhook_handler(secret_holder))
    threading.Thread(target=server.serve_forever, daemon=True).start()

    # Initialize MCP, then subscribe with auto-refresh.
    sid = session.initialize()
    print(f"Session: {sid}")
    print(f"Subscribing to {args.event} (auto-refresh at {args.refresh_factor:g}*TTL)...")

    refresh_count = [0]
    def on_refresh():
        refresh_count[0] += 1
        print(f"  [auto-refresh] subscription refreshed (count={refresh_count[0]})", flush=True)
    sub.on_refresh = on_refresh

    result = sub.start()
    if not args.secret:
        print(f"  using SDK-generated secret: {sub.secret[:16]}...")
    if result.get("id"):
        print(f"  subscription: {result['id']}")
    if result.get("refreshBefore"):
        print(f"  refreshBefore: {result['refreshBefore']}")

    print()
    print("Waiting for events (Ctrl-C to stop)...")
    print('  Inject in another terminal: make inject TEXT="hello"\n')

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopped.")
    finally:
        sub.stop()


# ═══════════════════════════════════════════════════════════════════
# Subcommand: poll
# ═══════════════════════════════════════════════════════════════════

def cmd_poll(session: MCPSession, args):
    if not args.event:
        sys.exit("ERROR: --event is required for poll mode")

    print("=== MCP Events — Poll Loop ===\n")

    sid = session.initialize()
    print(f"Session: {sid}")
    print(f"Polling {args.event} every {args.interval}s (Ctrl-C to stop)...")
    print('  Inject in another terminal: make inject TEXT="hello"\n')

    cursor = "0"
    try:
        while True:
            resp = session.rpc("events/poll", {
                "subscriptions": [{"id": "poll-loop", "name": args.event, "cursor": cursor}],
            })
            result = resp.get("result", {})
            events = result.get("events", [])
            if result.get("truncated"):
                print("  [!] truncated — server reset to a later position; some events were missed")
            if events:
                cursor = result.get("cursor", cursor)
                for ev in events:
                    print("── EVENT " + "─" * 45)
                    print(f"  id:      {ev.get('eventId', '')}")
                    print(f"  name:    {ev.get('name', '')}")
                    print(f"  time:    {ev.get('timestamp', '')}")
                    print(f"  cursor:  {_fmt_cursor(ev.get('cursor'))}")
                    data = ev.get("data")
                    if data:
                        print(json.dumps(data, indent=2))
                    print()
                sys.stdout.flush()
            elif cursor == "0":
                # First poll with cursor=0 — grab the cursor for next time
                cursor = result.get("cursor", cursor)
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\nStopped.")


# ═══════════════════════════════════════════════════════════════════
# CLI
# ═══════════════════════════════════════════════════════════════════

def main():
    parser = argparse.ArgumentParser(
        description="MCP Events client — list, listen, webhook, poll",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--mcp", default="http://localhost:8080/mcp", help="MCP endpoint URL")
    parser.add_argument("--event", default=None, help="Event name (e.g. discord.message)")
    parser.add_argument("--resource-uri", default=None, help="Resource URI for diag (e.g. discord://messages/recent)")

    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("list", help="Show server capabilities: tools, resources, events")

    sub.add_parser("listen", help="SSE push listener")

    wp = sub.add_parser("webhook", help="Webhook receiver (auto-refreshes subscription)")
    wp.add_argument("--port", type=int, default=9999, help="Local receiver port")
    wp.add_argument("--secret", default="", help="HMAC signing secret (whsec_ + base64 of 24-64 bytes per spec). Empty → SDK auto-generates.")
    wp.add_argument("--refresh-factor", type=float, default=0.5,
                    help="Refresh subscription at this fraction of TTL (default 0.5)")

    pp = sub.add_parser("poll", help="Polling loop")
    pp.add_argument("--interval", type=int, default=5, help="Seconds between polls")

    args = parser.parse_args()
    session = MCPSession(args.mcp)

    cmds = {"list": cmd_list, "listen": cmd_listen, "webhook": cmd_webhook, "poll": cmd_poll}
    cmds[args.command](session, args)


if __name__ == "__main__":
    main()

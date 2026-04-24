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
import hashlib
import hmac
import json
import sys
import textwrap
import threading
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.request import Request, urlopen


# ═══════════════════════════════════════════════════════════════════
# MCP session helpers
# ═══════════════════════════════════════════════════════════════════

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
        results = resp.get("result", {}).get("results", [])
        if results:
            r = results[0]
            n = len(r.get("events", []))
            print(f"  {n} event(s), cursor={r.get('cursor')}, hasMore={r.get('hasMore')}")
            for ev in r.get("events", [])[:3]:
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
            print(f"  cursor:  {p.get('cursor', '')}")
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
# Subcommand: webhook
# ═══════════════════════════════════════════════════════════════════

def _make_webhook_handler(secret: str):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)
            sig = self.headers.get("X-MCP-Signature", "")
            ts = self.headers.get("X-MCP-Timestamp", "")

            expected = "sha256=" + hmac.new(
                secret.encode(), (ts + ".").encode() + body, hashlib.sha256
            ).hexdigest()
            sig_ok = hmac.compare_digest(sig, expected)

            label = "sig OK" if sig_ok else "sig FAIL"
            print()
            print(f"── WEBHOOK EVENT ({label}) " + "─" * 30)
            try:
                event = json.loads(body)
                print(f"  id:      {event.get('eventId', '')}")
                print(f"  name:    {event.get('name', '')}")
                print(f"  time:    {event.get('timestamp', '')}")
                print(f"  cursor:  {event.get('cursor', '')}")
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

    secret = args.secret
    hook_url = f"http://localhost:{args.port}"

    print("=== MCP Events — Webhook Receiver ===\n")

    # Start local receiver first
    print(f"Starting webhook receiver on port {args.port}...")
    server = HTTPServer(("", args.port), _make_webhook_handler(secret))
    threading.Thread(target=server.serve_forever, daemon=True).start()

    # Initialize MCP + subscribe
    sid = session.initialize()
    print(f"Session: {sid}")
    print(f"Subscribing to {args.event}...")

    resp = session.rpc("events/subscribe", {
        "id": "wh-demo",
        "name": args.event,
        "delivery": {"mode": "webhook", "url": hook_url, "secret": secret},
    })
    result = resp.get("result", {})
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
            results = resp.get("result", {}).get("results", [])
            if results:
                r = results[0]
                events = r.get("events", [])
                if r.get("cursorGap"):
                    print("  [!] cursor gap — some events were missed")
                if events:
                    cursor = r.get("cursor", cursor)
                    for ev in events:
                        print("── EVENT " + "─" * 45)
                        print(f"  id:      {ev.get('eventId', '')}")
                        print(f"  name:    {ev.get('name', '')}")
                        print(f"  time:    {ev.get('timestamp', '')}")
                        print(f"  cursor:  {ev.get('cursor', '')}")
                        data = ev.get("data")
                        if data:
                            print(json.dumps(data, indent=2))
                        print()
                    sys.stdout.flush()
                elif cursor == "0":
                    # First poll with cursor=0 — grab the cursor for next time
                    cursor = r.get("cursor", cursor)
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

    wp = sub.add_parser("webhook", help="Webhook receiver")
    wp.add_argument("--port", type=int, default=9999, help="Local receiver port")
    wp.add_argument("--secret", default="demo-webhook-secret", help="HMAC shared secret")

    pp = sub.add_parser("poll", help="Polling loop")
    pp.add_argument("--interval", type=int, default=5, help="Seconds between polls")

    args = parser.parse_args()
    session = MCPSession(args.mcp)

    cmds = {"list": cmd_list, "listen": cmd_listen, "webhook": cmd_webhook, "poll": cmd_poll}
    cmds[args.command](session, args)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""TTL refresh smoke test driver.

Exercises the WebhookSubscription auto-refresh path against a discord-events
server running with a short TTL. Pass/fail criteria:

  1. Subscription survives past the original TTL boundary (proven by a
     successful event delivery after TTL has elapsed).
  2. At least one auto-refresh fires within the test window (on_refresh
     counter — fires for every successful subscribe call, including the
     initial one, so a healthy run lands at >= 2).

The Makefile target `make test-ttl` wraps this for POSIX hosts. On Windows
or any host without lsof/bash, run the server and this driver by hand:

    # terminal 1
    go run . -addr :18080 -webhook-ttl 3s

    # terminal 2
    python3 test_ttl.py --mcp http://localhost:18080/mcp \\
        --inject-url http://localhost:18080/inject \\
        --port 19999 --ttl 3 --duration 8

Exits 0 on success, non-zero on failure.
"""

import argparse
import http.server
import json
import socketserver
import sys
import threading
import time
from pathlib import Path

# Reuse the production helpers — we are testing them, after all.
# Demos live at examples/events/<name>/, the events client lives at
# experimental/ext/events/. Walk three parents up from this file to reach
# the repo root, then descend into experimental/ext/events.
sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "experimental" / "ext" / "events" / "clients" / "python"))
from events_client import MCPSession, WebhookSubscription, _make_webhook_handler  # noqa: E402


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--mcp", default="http://localhost:18080/mcp")
    p.add_argument("--inject-url", default="http://localhost:18080/inject")
    p.add_argument("--port", type=int, default=19999)
    p.add_argument("--ttl", type=int, default=3, help="server-side TTL seconds (must match server -webhook-ttl)")
    p.add_argument("--duration", type=float, default=8.0, help="total test wall-clock seconds (>= 2x TTL)")
    p.add_argument("--secret", default="ttl-test-secret")
    args = p.parse_args()

    if args.duration < args.ttl * 2:
        print(f"FAIL: duration {args.duration}s must be at least 2x TTL {args.ttl}s", file=sys.stderr)
        return 2

    received = []
    received_lock = threading.Lock()

    class CapturingHandler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):  # noqa: N802
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)
            with received_lock:
                received.append(body)
            self.send_response(200)
            self.end_headers()

        def log_message(self, *_args):
            pass

    httpd = socketserver.TCPServer(("", args.port), CapturingHandler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()

    session = MCPSession(args.mcp)
    session.initialize()

    refresh_count = [0]
    def on_refresh():
        refresh_count[0] += 1

    sub = WebhookSubscription(
        session,
        event_name="discord.message",
        callback_url=f"http://localhost:{args.port}",
        secret=args.secret,
        refresh_factor=0.5,  # refresh at 0.5*TTL (half a TTL window)
        on_refresh=on_refresh,
    )
    sub.start()

    # Sleep past the original TTL boundary to confirm the helper kept it alive.
    sleep_before_inject = args.ttl + 1.5
    print(f"[ttl-test] sleeping {sleep_before_inject:.1f}s (>1 original TTL)...")
    time.sleep(sleep_before_inject)

    # Inject a message — webhook should still be registered and receive it.
    inject_body = json.dumps({"guild_id": "g", "channel_id": "c", "sender": "ttl-test", "text": "alive after TTL"}).encode()
    try:
        from urllib.request import Request, urlopen
        req = Request(args.inject_url, data=inject_body, headers={"Content-Type": "application/json"})
        with urlopen(req, timeout=2) as resp:
            resp.read()
    except Exception as exc:
        print(f"FAIL: inject failed: {exc}", file=sys.stderr)
        return 3

    # Give the server a moment to deliver.
    time.sleep(1.0)
    sub.stop()
    httpd.shutdown()

    # Pass criteria. start() fires on_refresh once for the initial subscribe,
    # so any refresh past the original TTL boundary means count >= 2.
    print(f"[ttl-test] refresh callbacks fired: {refresh_count[0]}")
    print(f"[ttl-test] webhook deliveries received: {len(received)}")

    if refresh_count[0] < 2:
        print("FAIL: helper did not refresh at least once after the initial subscribe", file=sys.stderr)
        return 4
    if not received:
        print("FAIL: no webhook delivered after TTL boundary — subscription expired", file=sys.stderr)
        return 5

    print("[ttl-test] PASS — subscription survived past original TTL via auto-refresh")
    return 0


if __name__ == "__main__":
    sys.exit(main())

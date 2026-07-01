#!/usr/bin/env python3
"""A supervised component that deliberately leaks memory until it is killed.

It does two things:

  1. Serves HTTP 200 on every path (so the supervisor's health probe on the
     component port stays happy — /healthz, /readyz, etc.).
  2. In a background thread, keeps appending large byte buffers to a list so its
     resident memory climbs steadily.

Paired with a per-component `memory.hardlimit` in supervisor-leak.yml, the
supervisor's enforcer watches this component's memory STATE go pass -> warn ->
fail and, once it has been `fail` for `sustained_for`, performs its
`pressure_action` (graceful_restart). The fresh process starts small and climbs
again, so you can watch the restart loop and the recorded incidents in the
portal at http://127.0.0.1:9191/ and /backoffice/memory/incidents.

No cgroups are involved: on macOS the supervisor samples this process's RSS and
acts on it, which is exactly the point — supervisor-driven enforcement is
testable on a plain dev box.
"""

import http.server
import os
import signal
import socketserver
import sys
import threading
import time

SIZE = 2 * 1024 * 1024  # 3 MiB allocated per step
STEP = 10               # seconds between allocations

# Hold references so the memory is never freed — this is the leak. Each entry
# MUST be a fresh object: `b"x" * SIZE` builds a new, fully-resident buffer every
# call (unlike multiplying an existing buffer by 1, which CPython returns as the
# same object and would not grow RSS at all).
_hog = []


def _port():
    for arg in sys.argv[1:]:
        if arg.startswith("--port="):
            return int(arg.split("=", 1)[1])
    return int(os.environ.get("MONITOR_PORT") or os.environ.get("OP_MONITOR_PORT") or "8080")


def _leak():
    while True:
        _hog.append(b"\xab" * SIZE)  # a NEW, resident 3 MiB buffer each step
        print(f"[leaker] held ~{len(_hog) * SIZE // (1024 * 1024)} MiB", flush=True)
        time.sleep(STEP)


class _Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(b"ok\n")

    def log_message(self, *_args):
        pass  # keep the supervisor's captured stdout focused on the leak line


class _Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True  # rebind the port cleanly after a restart
    daemon_threads = True


def main():
    # Exit promptly on SIGTERM so graceful_restart is actually graceful.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))

    threading.Thread(target=_leak, daemon=True).start()

    port = _port()
    with _Server(("127.0.0.1", port), _Handler) as httpd:
        print(f"[leaker] serving health on 127.0.0.1:{port}; leaking {SIZE // (1024*1024)} MiB every {STEP}s", flush=True)
        httpd.serve_forever()


if __name__ == "__main__":
    main()

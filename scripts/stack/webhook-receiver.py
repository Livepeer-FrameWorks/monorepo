#!/usr/bin/env python3
"""Stack webhook receiver: records every POST, serves the log back for assertions.

POST <any path>     recorded to /data/requests.jsonl, answered with the status in
                    /data/status (default 204) so scenarios can force failures.
GET  /requests      the recorded deliveries as a JSON array.
PUT  /status        body is the HTTP status to answer POSTs with from now on.
DELETE /requests    clears the log.
"""
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DATA = os.environ.get("RECEIVER_DATA", "/data")
LOG = os.path.join(DATA, "requests.jsonl")
STATUS = os.path.join(DATA, "status")
LOCK = threading.Lock()


def reply_status():
    try:
        with open(STATUS) as f:
            return int(f.read().strip())
    except (OSError, ValueError):
        return 204


class Handler(BaseHTTPRequestHandler):
    def _body(self):
        return self.rfile.read(int(self.headers.get("Content-Length", 0) or 0)).decode("utf-8", "replace")

    def _send(self, code, payload=None):
        body = json.dumps(payload).encode() if payload is not None else b""
        self.send_response(code)
        if payload is not None:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = self._body()
        code = reply_status()
        with LOCK, open(LOG, "a") as f:
            f.write(json.dumps({"t": time.time(), "path": self.path, "headers": dict(self.headers),
                                "body": body, "reply": code}) + "\n")
        self._send(code)

    def do_GET(self):
        if self.path == "/healthz":
            return self._send(200, {"ok": True})
        if self.path != "/requests":
            return self._send(404, {"error": "not found"})
        with LOCK:
            try:
                with open(LOG) as f:
                    rows = [json.loads(line) for line in f if line.strip()]
            except OSError:
                rows = []
        self._send(200, rows)

    def do_PUT(self):
        if self.path != "/status":
            return self._send(404, {"error": "not found"})
        value = self._body().strip()
        if not value.isdigit():
            return self._send(400, {"error": "status must be numeric"})
        with open(STATUS, "w") as f:
            f.write(value)
        self._send(204)

    def do_DELETE(self):
        if self.path != "/requests":
            return self._send(404, {"error": "not found"})
        with LOCK, open(LOG, "w"):
            pass
        self._send(204)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    os.makedirs(DATA, exist_ok=True)
    ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("RECEIVER_PORT", "18777"))), Handler).serve_forever()

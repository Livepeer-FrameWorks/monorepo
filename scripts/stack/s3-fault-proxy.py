#!/usr/bin/env python3
"""Forward real S3 traffic; stall two PUTs for one explicitly armed artifact;
expire one armed artifact's presigned GETs signed before the arming instant;
stall one armed artifact's ranged media GET mid-body.

Only the private control port can arm the fixtures. Paths, queries, Host and
signed headers reach the real backend unchanged. Neither presigned URLs nor
credentials are logged.
"""
import calendar
import hmac
import http.client
import http.server
import json
import os
import select
import socket
import threading
import time
import urllib.parse


class Fault:
    def __init__(self):
        self.lock = threading.Lock()
        self.reset()

    def reset(self, artifact=""):
        with self.lock:
            self.artifact = artifact
            self.attempt = ""
            self.attempts = 0
            self.events = []
            self.epoch = time.monotonic()

    def begin(self, method, path):
        key = urllib.parse.unquote(urllib.parse.urlsplit(path).path)
        with self.lock:
            matched = (method == "PUT" and self.artifact
                       and f"/{self.artifact}/.staging/" in key)
            if not matched:
                return None, False
            attempt = key.split("/.staging/", 1)[1].split("/", 1)[0]
            if self.attempt and attempt != self.attempt:
                return None, False
            self.attempt = attempt
            name = key.rsplit("/", 1)[-1]
            stall = False
            if name == "sprite.jpg":
                self.attempts += 1
                stall = self.attempts <= 2
            self.events.append({"file": name, "event": "stalled" if stall else "started",
                                "at": time.monotonic() - self.epoch})
            return name, stall

    def accepted(self, name):
        if name is not None:
            with self.lock:
                self.events.append({"file": name, "event": "accepted",
                                    "at": time.monotonic() - self.epoch})

    def snapshot(self):
        with self.lock:
            return {"artifact": self.artifact, "attempt": self.attempt, "attempts": self.attempts,
                    "events": list(self.events)}


class Expiry:
    """Answers a presigned GET/HEAD for the armed artifact the way S3 answers
    an expired signature when its X-Amz-Date is before the arming second, as if
    every URL minted before then had reached its X-Amz-Expires."""

    def __init__(self):
        self.lock = threading.Lock()
        self.arm("")

    def arm(self, artifact, before=0):
        with self.lock:
            self.artifact = artifact
            self.before = before
            self.refused = 0
            self.passed = 0

    def refuses(self, method, path):
        if method not in ("GET", "HEAD"):
            return False
        parts = urllib.parse.urlsplit(path)
        query = urllib.parse.parse_qs(parts.query)
        if "X-Amz-Signature" not in query:
            return False
        with self.lock:
            if not self.artifact or self.artifact not in urllib.parse.unquote(parts.path):
                return False
            try:
                signed = calendar.timegm(time.strptime(query["X-Amz-Date"][0], "%Y%m%dT%H%M%SZ"))
            except (KeyError, ValueError):
                return False
            if signed < self.before:
                self.refused += 1
                return True
            self.passed += 1
            return False

    def snapshot(self):
        with self.lock:
            return {"artifact": self.artifact, "before": self.before,
                    "refused": self.refused, "passed": self.passed}


class Stall:
    """Stalls one ranged GET of the armed artifact's media mid-body: after
    STALL_AFTER bytes of the response it sends nothing more until the client
    gives up, the way a cold object-store read hangs. Only a response whose
    client is still reading at that point counts, so a reader that stops on its
    own (the relay's 4 KiB preflight) does not use the stall up. Requests that
    start inside the stalled range afterwards are recorded: only a resume of
    the stalled read starts there, block fetches start on block boundaries."""

    def __init__(self):
        self.lock = threading.Lock()
        self.arm("")

    def arm(self, artifact):
        with self.lock:
            self.artifact = artifact
            self.stalled = None
            self.skipped = 0
            self.resumes = []

    def candidate(self, method, path, headers):
        """The requested (start, end) when this GET may be the stalled one."""
        if method != "GET":
            return None
        key = urllib.parse.unquote(urllib.parse.urlsplit(path).path)
        rng = headers.get("Range", "")
        with self.lock:
            if not self.artifact or self.artifact not in key or key.endswith(".dtsh"):
                return None
            try:
                start, end = (int(v) for v in rng.removeprefix("bytes=").split("-", 1))
            except ValueError:
                return None
            if self.stalled is not None:
                if self.stalled["start"] < start <= self.stalled["end"]:
                    self.resumes.append({"start": start, "end": end})
                return None
            if end - start + 1 <= 2 * STALL_AFTER:
                return None
            return start, end

    def take(self, start, end, connection):
        """Claims the stall for this response unless its client already left."""
        time.sleep(0.3)
        if client_gone(connection):
            with self.lock:
                self.skipped += 1
            return False
        with self.lock:
            if self.stalled is not None:
                return False
            self.stalled = {"start": start, "end": end, "sent": STALL_AFTER}
            return True

    def snapshot(self):
        with self.lock:
            return {"artifact": self.artifact, "stalled": self.stalled,
                    "skipped": self.skipped, "resumes": list(self.resumes)}


def client_gone(connection):
    readable, _, _ = select.select([connection], [], [], 0)
    if not readable:
        return False
    try:
        return not connection.recv(1, socket.MSG_PEEK)
    except OSError:
        return True


def hold_until_client_leaves(connection, limit=90):
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        readable, _, _ = select.select([connection], [], [], 0.25)
        if readable and client_gone(connection):
            return


STALL_AFTER = 64 * 1024
FAULT = Fault()
EXPIRY = Expiry()
STALL = Stall()
EXPIRED_BODY = (b'<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code>'
                b'<Message>Request has expired</Message></Error>')
UPSTREAM = os.environ.get("STACK_S3_UPSTREAM", "s3-ceph:8080")
TOKEN = os.environ.get("SERVICE_TOKEN", "")
HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
               "te", "trailer", "transfer-encoding", "upgrade"}


class QuietHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass


class Control(QuietHandler):
    def handle_control(self):
        if not TOKEN or not hmac.compare_digest(self.headers.get("Authorization", ""), f"Bearer {TOKEN}"):
            self.send_error(403)
            return
        if self.command == "POST":
            try:
                payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
                for field in ("artifact", "expire_artifact", "stall_artifact"):
                    value = payload.get(field, "")
                    if not isinstance(value, str) or (value and not value.isalnum()):
                        raise ValueError("invalid artifact hash")
                if "artifact" in payload:
                    FAULT.reset(payload["artifact"])
                if "expire_artifact" in payload:
                    EXPIRY.arm(payload["expire_artifact"], int(time.time()))
                if "stall_artifact" in payload:
                    STALL.arm(payload["stall_artifact"])
            except (ValueError, TypeError, AttributeError):
                self.send_error(400)
                return
        body = json.dumps({**FAULT.snapshot(), "expiry": EXPIRY.snapshot(),
                           "stall": STALL.snapshot()}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = handle_control


class Proxy(QuietHandler):
    def body(self):
        if self.headers.get("Transfer-Encoding", "").lower() != "chunked":
            return self.rfile.read(int(self.headers.get("Content-Length", "0")))
        chunks = []
        while True:
            size = int(self.rfile.readline().split(b";", 1)[0], 16)
            if size == 0:
                while self.rfile.readline() not in (b"\r\n", b"\n", b""):
                    pass
                return b"".join(chunks)
            chunks.append(self.rfile.read(size))
            self.rfile.read(2)

    def forward(self):
        data = self.body()
        if EXPIRY.refuses(self.command, self.path):
            self.send_response_only(403, "Forbidden")
            self.send_header("Content-Type", "application/xml")
            self.send_header("Content-Length", str(len(EXPIRED_BODY)))
            self.send_header("Connection", "close")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(EXPIRED_BODY)
            self.close_connection = True
            return
        name, stall = FAULT.begin(self.command, self.path)
        if stall:
            # Do not commit the PUT. Wait until the client's own per-attempt
            # deadline closes its connection, allowing sibling requests to run.
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                readable, _, _ = select.select([self.connection], [], [], 0.25)
                if readable:
                    try:
                        if not self.connection.recv(1, socket.MSG_PEEK):
                            break
                    except OSError:
                        break
            self.close_connection = True
            return
        stall_range = STALL.candidate(self.command, self.path, self.headers)
        connection = http.client.HTTPConnection(UPSTREAM, timeout=120)
        try:
            headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_HEADERS}
            if self.headers.get("Transfer-Encoding"):
                headers["Content-Length"] = str(len(data))
            connection.request(self.command, self.path, body=data, headers=headers)
            response = connection.getresponse()
            if 200 <= response.status < 300:
                FAULT.accepted(name)
            self.send_response_only(response.status, response.reason)
            for key, value in response.getheaders():
                if key.lower() not in HOP_HEADERS:
                    self.send_header(key, value)
            self.send_header("Connection", "close")
            self.end_headers()
            if self.command != "HEAD":
                if stall_range is not None and response.status == 206:
                    self.wfile.write(response.read(STALL_AFTER))
                    self.wfile.flush()
                    if STALL.take(*stall_range, self.connection):
                        hold_until_client_leaves(self.connection)
                        return
                while chunk := response.read(65536):
                    self.wfile.write(chunk)
        except (OSError, http.client.HTTPException):
            self.close_connection = True
        finally:
            connection.close()
            self.close_connection = True

    do_GET = do_HEAD = do_PUT = do_POST = do_DELETE = do_OPTIONS = forward


def main():
    if not TOKEN:
        raise SystemExit("s3-fault-proxy needs SERVICE_TOKEN for its private control port")
    control = http.server.ThreadingHTTPServer(("0.0.0.0", 18881), Control)
    threading.Thread(target=control.serve_forever, daemon=True).start()
    http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Proxy).serve_forever()


if __name__ == "__main__":
    main()

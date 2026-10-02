#!/usr/bin/env python3
"""Forward real S3 traffic; stall two PUTs for one explicitly armed artifact.

Only the private control port can arm the thumbnail regression fixture. Paths,
queries, Host and signed headers reach the real backend unchanged. Neither
presigned URLs nor credentials are logged.
"""
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


FAULT = Fault()
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
                artifact = payload.get("artifact", "")
                if not isinstance(artifact, str) or (artifact and not artifact.isalnum()):
                    raise ValueError("invalid artifact hash")
                FAULT.reset(artifact)
            except (ValueError, TypeError):
                self.send_error(400)
                return
        body = json.dumps(FAULT.snapshot()).encode()
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
        connection = http.client.HTTPConnection(UPSTREAM, timeout=120)
        try:
            headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_HEADERS}
            if self.headers.get("Transfer-Encoding"):
                headers["Content-Length"] = str(len(data))
            connection.request(self.command, self.path, body=data, headers=headers)
            response = connection.getresponse()
            self.send_response_only(response.status, response.reason)
            for key, value in response.getheaders():
                if key.lower() not in HOP_HEADERS:
                    self.send_header(key, value)
            self.send_header("Connection", "close")
            self.end_headers()
            if self.command != "HEAD":
                while chunk := response.read(65536):
                    self.wfile.write(chunk)
            if 200 <= response.status < 300:
                FAULT.accepted(name)
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

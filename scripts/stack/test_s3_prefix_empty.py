#!/usr/bin/env python3
"""Verify which S3 listing outcomes s3-prefix-empty.py retries and which it refuses."""
import http.server
import os
import pathlib
import subprocess
import sys
import threading
import unittest

SCRIPT = pathlib.Path(__file__).with_name("s3-prefix-empty.py")

EMPTY = b'<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>'
ONE = (
    b'<?xml version="1.0"?><ListBucketResult><Contents><Key>vod/a.mp4</Key></Contents>'
    b"<IsTruncated>false</IsTruncated></ListBucketResult>"
)


class Store(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        mode = self.server.mode
        if mode == "reset":
            # A connection the store drops before answering, as a timed-out
            # or reset read does on the operator network.
            self.close_connection = True
            self.connection.shutdown(2)
            return
        status, body = {"empty": (200, EMPTY), "one": (200, ONE), "forbidden": (403, b"denied"), "unavailable": (503, b"busy")}[mode]
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class PrefixEmptyTest(unittest.TestCase):
    def setUp(self):
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Store)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def run_check(self, mode):
        self.server.mode = mode
        env = dict(os.environ, STACK_S3_ENDPOINT=f"http://127.0.0.1:{self.server.server_address[1]}")
        return subprocess.run([sys.executable, "-I", str(SCRIPT), "bucket", "vod/a"], env=env, capture_output=True, timeout=30).returncode

    def test_empty_prefix_passes(self):
        self.assertEqual(self.run_check("empty"), 0)

    def test_remaining_object_is_retried(self):
        self.assertEqual(self.run_check("one"), 1)

    def test_dropped_connection_is_retried(self):
        self.assertEqual(self.run_check("reset"), 1)

    def test_unavailable_store_is_retried(self):
        self.assertEqual(self.run_check("unavailable"), 1)

    def test_rejected_credentials_refuse(self):
        self.assertEqual(self.run_check("forbidden"), 2)


if __name__ == "__main__":
    unittest.main()

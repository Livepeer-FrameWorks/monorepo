#!/usr/bin/env python3
"""Verify forwarding and the scope of the thumbnail fault fixture."""
import http.client
import http.server
import importlib.util
import pathlib
import threading
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("s3_proxy", pathlib.Path(__file__).with_name("s3-fault-proxy.py"))
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)


MEDIA = bytes(i % 251 for i in range(512 * 1024))


class Backend(proxy.QuietHandler):
    def respond(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.seen.append((self.command, self.path, dict(self.headers), body))
        if "/media/" in self.path and self.headers.get("Range"):
            start, end = (int(v) for v in self.headers["Range"].removeprefix("bytes=").split("-"))
            self.send_response(206)
            self.send_header("Content-Range", f"bytes {start}-{end}/{len(MEDIA)}")
            self.send_header("Content-Length", str(end - start + 1))
            self.end_headers()
            self.wfile.write(MEDIA[start:end + 1])
            return
        self.send_response(200)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")

    do_GET = do_PUT = respond


class ProxyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.backend = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        cls.backend.seen = []
        proxy.UPSTREAM = "127.0.0.1:" + str(cls.backend.server_port)
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), proxy.Proxy)
        for server in (cls.backend, cls.server):
            threading.Thread(target=server.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        for server in (cls.server, cls.backend):
            server.shutdown()
            server.server_close()

    def setUp(self):
        proxy.FAULT.reset()
        proxy.EXPIRY.arm("")
        proxy.STALL.arm("")
        proxy.KEY_ORDER.arm("")
        self.backend.seen.clear()

    def ranged(self, path, start, end, timeout=3, read=None):
        """Status and body bytes of a ranged GET; read caps how much is read
        before the connection is closed."""
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=timeout)
        try:
            connection.request("GET", path, headers={"Range": f"bytes={start}-{end}"})
            response = connection.getresponse()
            got = b""
            try:
                while not read or len(got) < read:
                    chunk = response.read1(read - len(got) if read else 65536)
                    if not chunk:
                        break
                    got += chunk
            except TimeoutError:
                pass
            return response.status, got
        finally:
            connection.close()

    def test_stall_holds_one_live_ranged_read_mid_body_and_records_its_resume(self):
        proxy.STALL.arm("abc123")
        path = "/bucket/vod/abc123/media/abc123.mkv?X-Amz-Signature=s"
        end = len(MEDIA) - 1
        # A reader that stops after 4 KiB on its own does not use the stall up.
        status, got = self.ranged(path, 0, end, read=4096)
        self.assertEqual((status, got), (206, MEDIA[:4096]))
        time.sleep(0.5)
        self.assertIsNone(proxy.STALL.snapshot()["stalled"])
        # Probes, sidecars and other artifacts pass untouched.
        self.assertEqual(self.ranged(path, 0, 0)[1], MEDIA[:1])
        self.assertEqual(self.ranged("/bucket/vod/abc123/media/abc123.dtsh", 0, end)[1], MEDIA)
        self.assertEqual(self.ranged("/bucket/vod/other/media/other.mkv", 0, end)[1], MEDIA)
        # A live reader gets STALL_AFTER bytes and then nothing.
        began = time.monotonic()
        status, got = self.ranged(path, 0, end, timeout=1)
        self.assertEqual(status, 206)
        self.assertEqual(got, MEDIA[:proxy.STALL_AFTER])
        self.assertGreaterEqual(time.monotonic() - began, 1)
        self.assertEqual(proxy.STALL.snapshot()["stalled"]["start"], 0)
        # The resume from inside the stalled range, and every later read, is served.
        self.assertEqual(self.ranged(path, 100_000, end)[1], MEDIA[100_000:])
        self.assertEqual(self.ranged(path, 0, end)[1], MEDIA)
        self.assertEqual(proxy.STALL.snapshot()["resumes"], [{"start": 100_000, "end": end}])

    def test_resume_delay_holds_resumes_and_stalls_fresh_reads_again(self):
        proxy.STALL.arm("abc123", 0.5)
        path = "/bucket/vod/abc123/media/abc123.mkv?X-Amz-Signature=s"
        end = len(MEDIA) - 1
        status, got = self.ranged(path, 0, end, timeout=1)
        self.assertEqual((status, got), (206, MEDIA[:proxy.STALL_AFTER]))
        # A resume is answered in full, after the delay.
        began = time.monotonic()
        self.assertEqual(self.ranged(path, 100_000, end)[1], MEDIA[100_000:])
        self.assertGreaterEqual(time.monotonic() - began, 0.5)
        # A fresh read of the range stalls at the same byte and counts as a
        # restart; one that leaves on its own does not.
        self.assertEqual(self.ranged(path, 0, end, read=4096)[1], MEDIA[:4096])
        time.sleep(0.5)
        self.assertEqual(proxy.STALL.snapshot()["restarts"], 0)
        self.assertEqual(self.ranged(path, 0, end, timeout=1)[1], MEDIA[:proxy.STALL_AFTER])
        snapshot = proxy.STALL.snapshot()
        self.assertEqual(snapshot["restarts"], 1)
        self.assertEqual(snapshot["resumes"], [{"start": 100_000, "end": end}])

    def get(self, path):
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=2)
        try:
            connection.request("GET", path)
            response = connection.getresponse()
            return response.status, response.read()
        finally:
            connection.close()

    def test_expiry_refuses_only_the_armed_artifacts_older_presigned_gets(self):
        proxy.EXPIRY.arm("abc123", before=1_760_259_600)  # 2025-10-12T09:00:00Z
        old = "X-Amz-Date=20251012T085959Z&X-Amz-Expires=900&X-Amz-Signature=s"
        new = "X-Amz-Date=20251012T090000Z&X-Amz-Expires=900&X-Amz-Signature=s"
        status, body = self.get("/bucket/vod/abc123/abc123.mkv?" + old)
        self.assertEqual(status, 403)
        self.assertIn(b"Request has expired", body)
        self.assertEqual(self.get("/bucket/vod/abc123/abc123.mkv?" + new)[0], 200)
        self.assertEqual(self.get("/bucket/vod/other/other.mkv?" + old)[0], 200)
        self.assertEqual(self.get("/bucket/vod/abc123/abc123.mkv")[0], 200)
        self.assertEqual(proxy.EXPIRY.snapshot()["refused"], 1)
        self.assertEqual(len(self.backend.seen), 3)
        proxy.EXPIRY.arm("")
        self.assertEqual(self.get("/bucket/vod/abc123/abc123.mkv?" + old)[0], 200)

    def request(self, path, timeout=2, headers=None, body=b"thumbnail"):
        connection = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=timeout)
        try:
            connection.request("PUT", path, body=body, headers=headers or {})
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            self.assertEqual(response.read(), b"ok")
        finally:
            connection.close()

    def test_signed_url_host_headers_and_body_are_preserved(self):
        path = "/bucket/thumbnails/a%20b/sprite.jpg?X-Amz-Signature=signature&partNumber=1"
        headers = {"Host": "s3:8080", "Authorization": "signed-header",
                   "x-amz-content-sha256": "digest", "Content-Type": "image/jpeg"}
        self.request(path, headers=headers)
        method, seen_path, seen_headers, body = self.backend.seen[0]
        self.assertEqual((method, seen_path, body), ("PUT", path, b"thumbnail"))
        for key, value in headers.items():
            self.assertEqual(seen_headers[key], value)

    def test_acceptance_is_recorded_before_forwarding_acknowledgement(self):
        proxy.FAULT.reset("artifact")
        observed = []
        send_response = proxy.Proxy.send_response_only

        def observe_response(handler, *args, **kwargs):
            observed.append([event["file"] for event in proxy.FAULT.snapshot()["events"]
                             if event["event"] == "accepted"])
            return send_response(handler, *args, **kwargs)

        with mock.patch.object(proxy.Proxy, "send_response_only", observe_response):
            self.request("/bucket/thumbnails/artifact/.staging/attempt/poster.jpg")
        self.assertEqual(observed, [["poster.jpg"]])

    def test_only_two_attempts_of_one_artifact_stall(self):
        proxy.FAULT.reset("artifact")
        path = "/bucket/thumbnails/artifact/.staging/attempt/"
        for _ in range(2):
            with self.assertRaises(TimeoutError):
                self.request(path + "sprite.jpg", timeout=0.1)
        self.request(path + "poster.jpg")
        self.request(path + "sprite.vtt")
        self.request("/bucket/thumbnails/other/.staging/attempt/sprite.jpg")
        self.request("/bucket/thumbnails/artifact/.staging/different/sprite.jpg")
        self.request(path + "sprite.jpg")
        self.assertEqual(len(self.backend.seen), 5)
        snapshot = proxy.FAULT.snapshot()
        self.assertEqual(snapshot["attempts"], 3)
        self.assertEqual(snapshot["attempt"], "attempt")
        self.assertEqual([e["file"] for e in snapshot["events"] if e["event"] == "accepted"],
                         ["poster.jpg", "sprite.vtt", "sprite.jpg"])
        proxy.FAULT.reset()
        self.request(path + "sprite.jpg")
        self.assertEqual(proxy.FAULT.snapshot()["events"], [])

    def test_copies_to_the_armed_key_apply_in_order_past_their_clients(self):
        proxy.KEY_ORDER.arm("4f0c-asset", "sprite.jpg", hold=0.6, service=0.05)
        copy = {"x-amz-copy-source": "/bucket/thumbnails/4f0c-asset/v/tok/sprite.jpg"}
        served = "/bucket/thumbnails/4f0c-asset/sprite.jpg"
        with self.assertRaises(TimeoutError):
            self.request(served, timeout=0.2, headers=copy, body=b"")
        started = time.monotonic()
        self.request(served, timeout=3, headers=copy, body=b"")
        self.assertGreater(time.monotonic() - started, 0.3)
        self.request("/bucket/thumbnails/4f0c-asset/poster.jpg", headers=copy, body=b"")
        self.request(served, body=b"thumbnail")
        snapshot = proxy.KEY_ORDER.snapshot()
        self.assertEqual(snapshot["max_queued"], 2)
        self.assertEqual(snapshot["queued"], 0)
        self.assertEqual(len(snapshot["copies"]), 2)
        self.assertTrue(all("landed" in c for c in snapshot["copies"]))
        self.assertEqual(sum(1 for m, p, _, _ in self.backend.seen if p == served and m == "PUT"), 3)


if __name__ == "__main__":
    unittest.main()

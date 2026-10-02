#!/usr/bin/env python3
"""Verify forwarding and the scope of the thumbnail fault fixture."""
import http.client
import http.server
import importlib.util
import pathlib
import threading
import unittest

spec = importlib.util.spec_from_file_location("s3_proxy", pathlib.Path(__file__).with_name("s3-fault-proxy.py"))
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)


class Backend(proxy.QuietHandler):
    def respond(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.seen.append((self.command, self.path, dict(self.headers), body))
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
        self.backend.seen.clear()

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


if __name__ == "__main__":
    unittest.main()

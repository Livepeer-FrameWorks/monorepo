#!/usr/bin/env python3
"""Verify that a pinned URL reaches its pinned address under the URL's name."""
import asyncio
import http.server
import pathlib
import sys
import threading
import unittest
import urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).parent))
import replica_pin  # noqa: E402


class Echo(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def respond(self):
        self.server.seen.append((self.command, self.path, self.headers.get("Host")))
        body = b'{"ok":true}'
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = respond


class ReplicaPinTest(unittest.TestCase):
    def setUp(self):
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Echo)
        self.server.seen = []
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def test_unpinned_url_is_unchanged(self):
        self.assertEqual(replica_pin.split("https://bridge.example/graphql"), ("https://bridge.example/graphql", None))

    def test_join_keeps_the_pin(self):
        self.assertEqual(replica_pin.join("https://bridge.example#192.0.2.7", "/auth/login"), "https://bridge.example/auth/login#192.0.2.7")

    def test_websocket_url_of_a_pinned_base(self):
        self.assertEqual(replica_pin.ws_url("https://bridge.example#192.0.2.7"), "wss://bridge.example/graphql/ws#192.0.2.7")

    def test_pinned_request_reaches_the_pin_under_the_url_name(self):
        # bridge.invalid never resolves: the request can only arrive through the pin.
        url = f"http://bridge.invalid:{self.port}/graphql#127.0.0.1"
        req = urllib.request.Request(url, data=b"{}", headers={"Content-Type": "application/json"})
        with replica_pin.urlopen(req, timeout=5) as resp:
            self.assertEqual(resp.status, 200)
        self.assertEqual(self.server.seen, [("POST", "/graphql", f"bridge.invalid:{self.port}")])

    def test_pinned_opener_keeps_its_handlers(self):
        url = f"http://bridge.invalid:{self.port}/auth/login#127.0.0.1"
        opener = replica_pin.build_opener(url, urllib.request.HTTPCookieProcessor())
        with opener.open(replica_pin.split(url)[0], timeout=5) as resp:
            self.assertEqual(resp.status, 200)
        self.assertEqual(self.server.seen[-1][0:2], ("GET", "/auth/login"))

    def test_websocket_arguments_connect_to_the_pin(self):
        uri, kwargs = replica_pin.ws_args("wss://bridge.example/graphql/ws#192.0.2.7")
        self.assertEqual(uri, "wss://bridge.example/graphql/ws")
        self.assertEqual(kwargs, {"host": "192.0.2.7", "port": 443, "server_hostname": "bridge.example"})
        self.assertEqual(replica_pin.ws_args("ws://bridge-2:18000/graphql/ws"), ("ws://bridge-2:18000/graphql/ws", {}))


if __name__ == "__main__":
    asyncio.set_event_loop(asyncio.new_event_loop())
    unittest.main()

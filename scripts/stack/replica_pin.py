"""Reach one replica behind a service's public name.

A URL whose fragment is an address (https://bridge.example#192.0.2.7) is a
pinned URL: requests go to that address while TLS and the Host header keep the
URL's name, so the scenarios can address each replica of a deployed service
whose name resolves to all of them. Fragments are never sent, and an unpinned
URL behaves as before.
"""

import functools
import http.client
import socket
import urllib.parse
import urllib.request


def split(url):
    """(url without the pin, pinned address or None)."""
    parts = urllib.parse.urlsplit(url)
    return urllib.parse.urlunsplit(parts._replace(fragment="")), parts.fragment or None


def join(base, path):
    """base + path, keeping base's pin."""
    url, pin = split(base)
    return url.rstrip("/") + path + (f"#{pin}" if pin else "")


def ws_url(base):
    """The GraphQL WebSocket URL of a Bridge base URL, keeping its pin."""
    url = join(base, "/graphql/ws")
    return url.replace("https://", "wss://", 1).replace("http://", "ws://", 1)


def ws_args(url):
    """(uri, websockets.connect keyword arguments) for a possibly pinned URL."""
    uri, pin = split(url)
    if not pin:
        return uri, {}
    parts = urllib.parse.urlsplit(uri)
    secure = parts.scheme == "wss"
    kwargs = {"host": pin, "port": parts.port or (443 if secure else 80)}
    if secure:
        kwargs["server_hostname"] = parts.hostname
    return uri, kwargs


class _PinnedHTTP(http.client.HTTPConnection):
    def __init__(self, host, *, pin, **kwargs):
        super().__init__(host, **kwargs)
        self._pin = pin

    def connect(self):
        self.sock = socket.create_connection((self._pin, self.port), self.timeout)


class _PinnedHTTPS(http.client.HTTPSConnection):
    def __init__(self, host, *, pin, **kwargs):
        super().__init__(host, **kwargs)
        self._pin = pin

    def connect(self):
        sock = socket.create_connection((self._pin, self.port), self.timeout)
        self.sock = self._context.wrap_socket(sock, server_hostname=self.host)


class _PinnedHTTPHandler(urllib.request.HTTPHandler):
    def __init__(self, pin):
        super().__init__()
        self._pin = pin

    def http_open(self, req):
        return self.do_open(functools.partial(_PinnedHTTP, pin=self._pin), req)


class _PinnedHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, pin):
        super().__init__()
        self._pin = pin

    def https_open(self, req):
        return self.do_open(functools.partial(_PinnedHTTPS, pin=self._pin), req, context=self._context)


def build_opener(url, *handlers):
    """urllib.request.build_opener(*handlers), sending to url's pin if it has one."""
    _, pin = split(url)
    if pin:
        handlers = (*handlers, _PinnedHTTPHandler(pin), _PinnedHTTPSHandler(pin))
    return urllib.request.build_opener(*handlers)


def urlopen(req, timeout):
    """urllib.request.urlopen for a possibly pinned request or URL."""
    url = req.full_url if isinstance(req, urllib.request.Request) else req
    clean, pin = split(url)
    if not pin:
        return urllib.request.urlopen(req, timeout=timeout)
    if isinstance(req, urllib.request.Request):
        req.full_url = clean
    else:
        req = clean
    return build_opener(url).open(req, timeout=timeout)

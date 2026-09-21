"""Shared helpers: every test loads the language-neutral fixtures in sdk_conformance/."""

from __future__ import annotations

import json
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx

CONFORMANCE = Path(__file__).resolve().parents[2] / "sdk_conformance"

_FIELDS = {
    "status": "status",
    "code": "code",
    "message": "message",
    "retryAfterSeconds": "retry_after_seconds",
    "typename": "typename",
    "field": "field",
    "resourceType": "resource_type",
    "resourceId": "resource_id",
    "path": "path",
    "serverVersion": "server_version",
    "minimumVersion": "minimum_version",
    "operation": "operation",
    "since": "since",
}


def load_fixture(name: str) -> Any:
    return json.loads((CONFORMANCE / name).read_text())


def error_mismatches(err: BaseException | None, expected: dict[str, Any]) -> list[str]:
    """Differences between an error and a fixture's expected kind and fields."""
    problems = []
    if type(err).__name__ != expected["kind"]:
        problems.append(f"kind {type(err).__name__} ({err}), want {expected['kind']}")
    for key, want in expected.items():
        if key == "kind":
            continue
        attr = _FIELDS.get(key)
        if attr is None:
            problems.append(f"fixture field {key} has no mapping")
            continue
        got = getattr(err, attr, None)
        if got != want:
            problems.append(f"{key} {got!r}, want {want!r}")
    return problems


def network_failure(kind: Any) -> httpx.TransportError:
    """The httpx error for a fixture networkError; true means reset."""
    if kind is True:
        kind = "reset"
    failures: dict[str, httpx.TransportError] = {
        "refused": httpx.ConnectError("[Errno 61] Connection refused"),
        "dns": httpx.ConnectError("[Errno 8] nodename nor servname provided, or not known"),
        "reset": httpx.ReadError("[Errno 54] Connection reset by peer"),
        "timeout": httpx.ReadTimeout("timed out"),
    }
    return failures[kind]


def fixture_response(r: dict[str, Any]) -> httpx.Response:
    """Answers one scripted response, or raises the way httpx does on a network failure."""
    if r.get("networkError"):
        raise network_failure(r["networkError"])
    text = r["bodyText"] if "bodyText" in r else ("" if "body" not in r else json.dumps(r["body"]))
    return httpx.Response(r.get("status", 200), headers=r.get("headers"), content=text.encode())


class Scripted:
    """An httpx transport answering from queues per operation name and recording every request."""

    def __init__(self, queues: dict[str, list[dict[str, Any]]]) -> None:
        self.queues = {k: list(v) for k, v in queues.items()}
        self.requests: list[tuple[str | None, httpx.Headers, dict[str, Any]]] = []

    def handler(self, request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content)
        name = body.get("operationName")
        self.requests.append((name, request.headers, body))
        queue = self.queues.get(name or "") or self.queues.get("*")
        if not queue:
            raise AssertionError(f"no scripted response left for {name}")
        return fixture_response(queue.pop(0))

    def sync_client(self) -> httpx.Client:
        return httpx.Client(transport=httpx.MockTransport(self.handler))

    def async_client(self) -> httpx.AsyncClient:
        return httpx.AsyncClient(transport=httpx.MockTransport(self.handler))

    def sent(self, name: str) -> int:
        return sum(1 for n, _, _ in self.requests if n == name)


Sleeps = Callable[[float], None]

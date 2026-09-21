"""sdk_conformance/webhooks.json, plus a round trip against a local HTTP server."""

from __future__ import annotations

import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import httpx
import pytest
import standardwebhooks.webhooks as standard_webhooks
from standardwebhooks import Webhook

from conftest import load_fixture
from livepeer_frameworks import WebhookEvent, WebhookReceiver, WebhookVerificationError, parse_webhook_event

FIXTURE = load_fixture("webhooks.json")


def _freeze(monkeypatch: pytest.MonkeyPatch, unix: int) -> None:
    """Pins the clock the Standard Webhooks library checks timestamps against."""

    class FrozenDatetime(datetime):
        @classmethod
        def now(cls, tz: Any = None) -> FrozenDatetime:
            return cls.fromtimestamp(unix, tz=tz or timezone.utc)

    monkeypatch.setattr(standard_webhooks, "datetime", FrozenDatetime)


@pytest.mark.parametrize("case", FIXTURE["verify"], ids=lambda c: c["name"])
def test_verify(case: dict[str, Any], monkeypatch: pytest.MonkeyPatch) -> None:
    _freeze(monkeypatch, case["now"])
    receiver = WebhookReceiver(case["secret"])
    if case["valid"]:
        receiver.verify(case["body"], case["headers"])
    else:
        with pytest.raises(WebhookVerificationError):
            receiver.verify(case["body"], case["headers"])


def _json_path(data: Any, path: str) -> Any:
    for part in path.split("."):
        data = data.get(part) if isinstance(data, dict) else None
    return data


@pytest.mark.parametrize("case", FIXTURE["parse"], ids=lambda c: c["name"])
def test_parse(case: dict[str, Any]) -> None:
    event = parse_webhook_event(case["body"])
    expect = case["expect"]
    assert (event.known, event.id, event.type, event.api_version, event.created_at) == (
        expect["known"],
        expect["id"],
        expect["type"],
        expect["apiVersion"],
        expect["createdAt"],
    )
    if expect["known"]:
        assert event.data is not None
        # The protobuf JSON form: lowerCamel names, 64-bit integers as strings, enums by name.
        proto_json = event.data.to_dict()
        for path, want in expect.get("expectFields", {}).items():
            assert _json_path(proto_json, path) == want, path
    else:
        assert event.data is None
        assert event.raw_data == expect["rawData"]


def test_round_trip_against_local_server() -> None:
    secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
    receiver = WebhookReceiver(secret)
    received: list[WebhookEvent] = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802
            body = self.rfile.read(int(self.headers["content-length"]))
            try:
                received.append(receiver.receive(body, dict(self.headers.items())))
                self.send_response(204)
            except WebhookVerificationError:
                self.send_response(401)
            self.end_headers()

        def log_message(self, *args: Any) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        url = f"http://127.0.0.1:{server.server_address[1]}/"
        body = FIXTURE["parse"][1]["body"]
        msg_id = "0199a3c3-0000-7000-8000-000000000001"
        now = datetime.now(tz=timezone.utc)

        def send(signature: str) -> int:
            return httpx.post(
                url,
                content=body,
                headers={
                    "content-type": "application/json",
                    "webhook-id": msg_id,
                    "webhook-timestamp": str(int(now.timestamp())),
                    "webhook-signature": signature,
                },
            ).status_code

        assert send(Webhook(secret).sign(msg_id, now, body)) == 204
        assert send(Webhook("whsec_YW5vdGhlciBzZWNyZXQga2V5ISE=").sign(msg_id, now, body)) == 401
        assert [e.type for e in received] == ["stream.live"]
    finally:
        server.shutdown()

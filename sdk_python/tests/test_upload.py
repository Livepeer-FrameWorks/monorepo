"""sdk_conformance/upload.json against a local HTTP server that serves GraphQL and the part URLs."""

from __future__ import annotations

import json
import socket
import threading
import time
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

import httpx
import pytest

from conftest import error_mismatches, load_fixture
from livepeer_frameworks import AsyncFrameWorksClient, FrameWorksClient, async_upload_vod, upload_vod

FIXTURE = load_fixture("upload.json")
CASES = FIXTURE["cases"]
SIGNATURE = FIXTURE["presignedSignature"]


def _error_text(err: BaseException) -> str:
    """The messages of an error and of every error it wraps."""
    parts: list[str] = []
    seen: BaseException | None = err
    while seen is not None:
        parts.append(str(seen))
        seen = seen.__cause__ or seen.__context__
    return "\n".join(parts)

VOD_ASSET = {
    "__typename": "VodAsset",
    "id": "vod-1",
    "artifactHash": "hash",
    "playbackId": "play",
    "streamId": None,
    "title": None,
    "description": None,
    "filename": "file.mp4",
    "status": "PROCESSING",
    "sizeBytes": 10,
    "durationMs": None,
    "resolution": None,
    "videoCodec": None,
    "audioCodec": None,
    "bitrateKbps": None,
    "createdAt": "2026-09-19T14:03:27Z",
    "updatedAt": "2026-09-19T14:03:27Z",
    "expiresAt": None,
    "errorMessage": None,
    "playbackPolicy": None,
    "thumbnailAssets": None,
    "effectiveRetention": None,
}


class UploadServer:
    """Serves createVodUpload, completeVodUpload, abortVodUpload, and the part PUTs of one case."""

    def __init__(self, case: dict[str, Any]) -> None:
        self.case = case
        self.puts: dict[str, int] = {}
        self.part_bytes: dict[str, int] = {}
        self.failures = {k: list(v) for k, v in case.get("putFailures", {}).items()}
        self.drops = dict(case.get("putDrops", {}))
        self.in_flight = 0
        self.max_in_flight = 0
        self.aborted = False
        self.completed = False
        self.completed_parts: Any = None
        self.file_ok = True
        self.lock = threading.Lock()
        server = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:
                pass

            def _body(self) -> bytes:
                return self.rfile.read(int(self.headers.get("content-length") or 0))

            def do_PUT(self) -> None:  # noqa: N802
                server.put(self)

            def do_POST(self) -> None:  # noqa: N802
                server.graphql(self)

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.origin = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        threading.Thread(target=self.httpd.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()

    def put(self, handler: Any) -> None:
        body = handler._body()
        part = parse_qs(urlparse(handler.path).query)["part"][0]
        with self.lock:
            self.puts[part] = self.puts.get(part, 0) + 1
            self.in_flight += 1
            self.max_in_flight = max(self.max_in_flight, self.in_flight)
        time.sleep(0.005)
        with self.lock:
            self.in_flight -= 1
            drop = self.drops.get(part, 0) > 0
            if drop:
                self.drops[part] -= 1
            failure = self.failures.get(part, [None]).pop(0) if self.failures.get(part) and not drop else None
        if drop:
            handler.close_connection = True
            handler.connection.shutdown(socket.SHUT_RDWR)
            return
        if failure:
            handler.send_response(failure)
            if "putRetryAfter" in self.case:
                handler.send_header("Retry-After", self.case["putRetryAfter"])
            handler.end_headers()
            return
        offset = (int(part) - 1) * self.case["partSize"]
        if any(b != (offset + i) % 251 for i, b in enumerate(body)):
            self.file_ok = False
        self.part_bytes[part] = len(body)
        handler.send_response(200)
        handler.send_header("ETag", f'"etag-{part}"')
        handler.end_headers()

    def graphql(self, handler: Any) -> None:
        request = json.loads(handler._body())
        name = request["operationName"]
        parts = -(-self.case["sizeBytes"] // self.case["partSize"])
        if name == "CreateVodUpload":
            data = {
                "createVodUpload": self.case.get("createResult")
                or {
                    "__typename": "VodUploadSession",
                    "id": "upload-1",
                    "artifactId": "artifact-1",
                    "artifactHash": "hash",
                    "playbackId": "play",
                    "partSize": self.case["partSize"],
                    "parts": [
                        {"partNumber": i + 1, "presignedUrl": f"{self.origin}/s3?part={i + 1}&X-Amz-Signature={SIGNATURE}"}
                        for i in range(parts)
                    ],
                    "expiresAt": "2026-09-20T14:03:27Z",
                }
            }
        elif name == "CompleteVodUpload":
            self.completed = True
            self.completed_parts = request["variables"]["input"]["parts"]
            data = {"completeVodUpload": self.case.get("completeResult") or VOD_ASSET}
        else:
            self.aborted = True
            data = {
                "abortVodUpload": {"__typename": "DeleteSuccess", "success": True, "deletedId": "upload-1", "pending": None}
            }
        payload = json.dumps({"data": data}).encode()
        handler.send_response(200)
        handler.send_header("content-type", "application/json")
        handler.send_header("content-length", str(len(payload)))
        handler.end_headers()
        handler.wfile.write(payload)

    def check(self, outcome: Any) -> None:
        expect = self.case["expect"]
        if expect["ok"]:
            assert not isinstance(outcome, BaseException), repr(outcome)
            assert outcome.model_dump(by_alias=True, mode="json") == VOD_ASSET
        else:
            assert isinstance(outcome, BaseException), "upload succeeded"
            assert error_mismatches(outcome, expect["error"]) == []
            if "errorOmits" in expect:
                assert expect["errorOmits"] not in _error_text(outcome)
        for key, got in (
            ("puts", self.puts),
            ("partBytes", self.part_bytes),
            ("completedParts", self.completed_parts),
            ("completed", self.completed),
        ):
            if key in expect:
                assert got == expect[key], key
        if "maxConcurrentPuts" in expect:
            assert self.max_in_flight <= expect["maxConcurrentPuts"]
        assert self.aborted == expect["aborted"]
        assert self.file_ok


@pytest.fixture
def server_for() -> Iterator[Any]:
    servers: list[UploadServer] = []

    def make(case: dict[str, Any]) -> UploadServer:
        servers.append(UploadServer(case))
        return servers[-1]

    yield make
    for s in servers:
        s.httpd.shutdown()


def _file(case: dict[str, Any]) -> bytes:
    return bytes(i % 251 for i in range(case["sizeBytes"]))


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
def test_upload_sync(case: dict[str, Any], server_for: Any) -> None:
    server = server_for(case)
    client = FrameWorksClient(f"{server.origin}/graphql", token="t", check_server=False)
    outcome: Any
    try:
        outcome = upload_vod(
            client,
            _file(case),
            filename="file.mp4",
            concurrency=case["concurrency"],
            part_attempts=case["partAttempts"],
            _sleep=lambda _: None,
        )
    except Exception as err:  # noqa: BLE001
        outcome = err
    server.check(outcome)


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
async def test_upload_async(case: dict[str, Any], server_for: Any) -> None:
    server = server_for(case)
    client = AsyncFrameWorksClient(f"{server.origin}/graphql", token="t", check_server=False)

    async def no_wait(_: float) -> None:
        return None

    outcome: Any
    try:
        outcome = await async_upload_vod(
            client,
            _file(case),
            filename="file.mp4",
            concurrency=case["concurrency"],
            part_attempts=case["partAttempts"],
            _sleep=no_wait,
        )
    except Exception as err:  # noqa: BLE001
        outcome = err
    server.check(outcome)


def test_upload_error_never_quotes_the_presigned_query(server_for: Any) -> None:
    case = {"sizeBytes": 4, "partSize": 4, "expect": {}}
    server = server_for(case)
    client = FrameWorksClient(f"{server.origin}/graphql", token="t", check_server=False)

    def refuse(request: httpx.Request) -> httpx.Response:
        # A transport error that quotes the URL it could not reach.
        raise httpx.ConnectError(f"cannot connect to {request.url}", request=request)

    with pytest.raises(Exception) as caught:
        upload_vod(
            client,
            _file(case),
            filename="file.mp4",
            part_attempts=1,
            http_client=httpx.Client(transport=httpx.MockTransport(refuse)),
            _sleep=lambda _: None,
        )
    assert type(caught.value).__name__ == "UploadError"
    text = _error_text(caught.value)
    assert SIGNATURE not in text, text
    assert f"{server.origin}/s3" in text

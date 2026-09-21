"""VOD upload helper: createVodUpload, a PUT of every part to its presigned
URL (a bounded number at once, each retried on network errors, 408, 429, and
5xx, waiting a storage Retry-After up to the client's max_retry_after_ms),
then completeVodUpload with the part ETags. Any failure after the upload is
created aborts it with abortVodUpload before the error is raised. Errors
never carry a presigned URL's query."""

from __future__ import annotations

import asyncio
import os
import threading
import time
from collections.abc import Awaitable, Callable
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import IO, TYPE_CHECKING, Any, Union
from urllib.parse import urlsplit

import httpx

from .errors import FrameWorksError, NetworkError, UploadError
from .results import expect_result
from .retry import DEFAULT_RETRY_POLICY, RETRYABLE_STATUSES, RetryPolicy, backoff_delay_ms, parse_retry_after

if TYPE_CHECKING:
    from ._generated.graphql.async_client import AsyncGraphQLClient
    from ._generated.graphql.client import GraphQLClient
    from ._generated.graphql.fragments import VodAssetFields

#: Bytes, a file path, or a binary file opened for reading.
UploadSource = Union[bytes, bytearray, memoryview, str, "os.PathLike[str]", IO[bytes]]


class _Reader:
    """Reads byte ranges of the source; file reads are serialized because they seek."""

    def __init__(self, source: UploadSource) -> None:
        self._lock = threading.Lock()
        self._owned: IO[bytes] | None = None
        self._bytes: bytes | None = None
        self._file: IO[bytes] | None = None
        if isinstance(source, (bytes, bytearray, memoryview)):
            self._bytes = bytes(source)
            self.size = len(self._bytes)
        elif isinstance(source, (str, os.PathLike)):
            self._owned = self._file = open(source, "rb")  # noqa: SIM115
            self.size = os.fstat(self._file.fileno()).st_size
        else:
            self._file = source
            self._file.seek(0, os.SEEK_END)
            self.size = self._file.tell()

    def read(self, start: int, end: int) -> bytes:
        if self._bytes is not None:
            return self._bytes[start:end]
        assert self._file is not None
        with self._lock:
            self._file.seek(start)
            return self._file.read(end - start)

    def close(self) -> None:
        if self._owned is not None:
            self._owned.close()


@dataclass(frozen=True)
class _Part:
    number: int
    url: str
    start: int
    end: int


def _parts(session: Any, size: int) -> list[_Part]:
    part_size = int(session.part_size)
    parts = sorted(session.parts, key=lambda p: p.part_number)
    return [
        _Part(p.part_number, p.presigned_url, (p.part_number - 1) * part_size, min(p.part_number * part_size, size))
        for p in parts
    ]


def _outcome(upload_id: str, part: _Part, response: httpx.Response) -> tuple[str | None, UploadError | None, int | None]:
    """The part's ETag, or the error and its Retry-After; a non-retryable status raises."""
    etag = response.headers.get("etag")
    if response.is_success and etag:
        return etag, None, None
    message = (
        f"part {part.number} returned no ETag"
        if response.is_success
        else f"part {part.number} failed with HTTP {response.status_code}"
    )
    failure = UploadError(message, upload_id, part.number, status=response.status_code)
    if response.status_code not in RETRYABLE_STATUSES:
        raise failure
    return None, failure, parse_retry_after(response.headers.get("retry-after"))


def _redact(text: str, url: str) -> str:
    """Replaces url, and its query, in text with the URL's origin and path: a
    presigned URL's query is a credential."""
    split = urlsplit(url)
    visible = f"{split.scheme}://{split.netloc}{split.path}" if split.netloc else "[presigned URL]"
    text = text.replace(url, visible)
    if split.query:
        text = text.replace(split.query, "")
    return text


def _transport_failure(upload_id: str, part: _Part, err: httpx.TransportError) -> UploadError:
    """The error of a part whose PUT failed in transport, without the presigned URL."""
    description = _redact(str(err), part.url)
    failure = UploadError(f"part {part.number} failed: {description}", upload_id, part.number)
    failure.__cause__ = NetworkError(description)
    return failure


def _waits_too_long(retry_after: int | None, policy: RetryPolicy) -> bool:
    """A storage Retry-After above the client's GraphQL maximum ends
    retrying instead of waiting, as it does for GraphQL requests."""
    return retry_after is not None and retry_after * 1000 > policy.max_retry_after_ms


def upload_vod(
    client: GraphQLClient,
    source: UploadSource,
    *,
    filename: str,
    content_type: str | None = None,
    title: str | None = None,
    description: str | None = None,
    concurrency: int = 4,
    part_attempts: int = 3,
    on_progress: Callable[[int, int], None] | None = None,
    http_client: httpx.Client | None = None,
    _sleep: Callable[[float], None] = time.sleep,
) -> VodAssetFields:
    """Uploads a file as a VOD asset and returns the asset completeVodUpload returned."""
    from ._generated.graphql.complete_vod_upload import CompleteVodUploadCompleteVodUploadVodAsset
    from ._generated.graphql.create_vod_upload import CreateVodUploadCreateVodUploadVodUploadSession
    from ._generated.graphql.input_types import (
        CompleteVodUploadInput,
        CreateVodUploadInput,
        VodUploadCompletedPart,
    )

    reader = _Reader(source)
    put_client = http_client or httpx.Client()
    try:
        fields: dict[str, Any] = {"filename": filename, "size_bytes": reader.size}
        for key, value in (("content_type", content_type), ("title", title), ("description", description)):
            if value is not None:
                fields[key] = value
        created = client.create_vod_upload(input=CreateVodUploadInput(**fields))
        session = expect_result(created.create_vod_upload, CreateVodUploadCreateVodUploadVodUploadSession)
        try:
            parts = _parts(session, reader.size)
            etags: dict[int, str] = {}
            state = {"next": 0, "uploaded": 0, "stopped": False}
            lock = threading.Lock()
            attempts = max(1, part_attempts)

            def send(part: _Part) -> None:
                for attempt in range(1, attempts + 1):
                    retry_after: int | None = None
                    try:
                        response = put_client.put(part.url, content=reader.read(part.start, part.end))
                        etag, failure, retry_after = _outcome(session.id, part, response)
                    except httpx.TransportError as err:
                        etag, failure = None, _transport_failure(session.id, part, err)
                    if etag is not None:
                        with lock:
                            etags[part.number] = etag
                            state["uploaded"] += part.end - part.start
                            uploaded = state["uploaded"]
                        if on_progress:
                            on_progress(int(uploaded), reader.size)
                        return
                    assert failure is not None
                    if attempt >= attempts or _waits_too_long(retry_after, client.retry):
                        raise failure
                    delay = retry_after * 1000 if retry_after is not None else backoff_delay_ms(DEFAULT_RETRY_POLICY, attempt)
                    _sleep(delay / 1000)

            def worker() -> None:
                # After the first failed part no worker starts another one.
                while True:
                    with lock:
                        if state["stopped"] or state["next"] >= len(parts):
                            return
                        part = parts[int(state["next"])]
                        state["next"] += 1
                    try:
                        send(part)
                    except BaseException:
                        with lock:
                            state["stopped"] = True
                        raise

            workers = max(1, min(concurrency, len(parts)))
            with ThreadPoolExecutor(max_workers=workers) as pool:
                futures = [pool.submit(worker) for _ in range(workers)]
            for future in futures:
                future.result()

            completed = client.complete_vod_upload(
                input=CompleteVodUploadInput(
                    upload_id=session.id,
                    parts=[VodUploadCompletedPart(part_number=p.number, etag=etags.get(p.number, "")) for p in parts],
                )
            )
            return expect_result(completed.complete_vod_upload, CompleteVodUploadCompleteVodUploadVodAsset)
        except BaseException:
            try:
                client.abort_vod_upload(upload_id=session.id)
            except FrameWorksError:
                pass
            raise
    finally:
        reader.close()
        if http_client is None:
            put_client.close()


async def async_upload_vod(
    client: AsyncGraphQLClient,
    source: UploadSource,
    *,
    filename: str,
    content_type: str | None = None,
    title: str | None = None,
    description: str | None = None,
    concurrency: int = 4,
    part_attempts: int = 3,
    on_progress: Callable[[int, int], None] | None = None,
    http_client: httpx.AsyncClient | None = None,
    _sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
) -> VodAssetFields:
    """Async upload_vod."""
    from ._generated.graphql.complete_vod_upload import CompleteVodUploadCompleteVodUploadVodAsset
    from ._generated.graphql.create_vod_upload import CreateVodUploadCreateVodUploadVodUploadSession
    from ._generated.graphql.input_types import (
        CompleteVodUploadInput,
        CreateVodUploadInput,
        VodUploadCompletedPart,
    )

    reader = _Reader(source)
    put_client = http_client or httpx.AsyncClient()
    try:
        fields: dict[str, Any] = {"filename": filename, "size_bytes": reader.size}
        for key, value in (("content_type", content_type), ("title", title), ("description", description)):
            if value is not None:
                fields[key] = value
        created = await client.create_vod_upload(input=CreateVodUploadInput(**fields))
        session = expect_result(created.create_vod_upload, CreateVodUploadCreateVodUploadVodUploadSession)
        try:
            parts = _parts(session, reader.size)
            etags: dict[int, str] = {}
            state = {"next": 0, "uploaded": 0, "stopped": False}
            attempts = max(1, part_attempts)

            async def send(part: _Part) -> None:
                for attempt in range(1, attempts + 1):
                    retry_after: int | None = None
                    try:
                        response = await put_client.put(part.url, content=reader.read(part.start, part.end))
                        etag, failure, retry_after = _outcome(session.id, part, response)
                    except httpx.TransportError as err:
                        etag, failure = None, _transport_failure(session.id, part, err)
                    if etag is not None:
                        etags[part.number] = etag
                        state["uploaded"] += part.end - part.start
                        if on_progress:
                            on_progress(int(state["uploaded"]), reader.size)
                        return
                    assert failure is not None
                    if attempt >= attempts or _waits_too_long(retry_after, client.retry):
                        raise failure
                    delay = retry_after * 1000 if retry_after is not None else backoff_delay_ms(DEFAULT_RETRY_POLICY, attempt)
                    await _sleep(delay / 1000)

            async def worker() -> None:
                while not state["stopped"] and state["next"] < len(parts):
                    part = parts[int(state["next"])]
                    state["next"] += 1
                    try:
                        await send(part)
                    except BaseException:
                        state["stopped"] = True
                        raise

            workers = max(1, min(concurrency, len(parts)))
            results = await asyncio.gather(*(worker() for _ in range(workers)), return_exceptions=True)
            for result in results:
                if isinstance(result, BaseException):
                    raise result

            completed = await client.complete_vod_upload(
                input=CompleteVodUploadInput(
                    upload_id=session.id,
                    parts=[VodUploadCompletedPart(part_number=p.number, etag=etags.get(p.number, "")) for p in parts],
                )
            )
            return expect_result(completed.complete_vod_upload, CompleteVodUploadCompleteVodUploadVodAsset)
        except BaseException:
            try:
                await client.abort_vod_upload(upload_id=session.id)
            except FrameWorksError:
                pass
            raise
    finally:
        reader.close()
        if http_client is None:
            await put_client.aclose()

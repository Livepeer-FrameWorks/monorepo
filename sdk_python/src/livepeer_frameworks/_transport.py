"""The transport under the generated clients: construction, auth, retries,
the serverInfo gate, typed errors, and WebSocket subscriptions.

The generated GraphQLClient and AsyncGraphQLClient subclass SyncTransport
and AsyncTransport. Every generated method passes its keyword arguments to
execute, so per-call options travel as keywords on the generated methods:

    client.delete_stream(id="...", idempotency_key="delete-7f3a2c")
    client.resolve_viewer_endpoint(content_id="...", playback_token=jwt)
"""

from __future__ import annotations

import asyncio
import inspect
import json
import time
import uuid
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping
from dataclasses import dataclass, replace
from typing import Any, Literal, TypeVar, Union

import httpx
from graphql import get_operation_ast, parse
from pydantic import BaseModel as PydanticBaseModel
from pydantic_core import to_jsonable_python

from ._generated.manifest import MIN_SERVER_VERSION, OPERATIONS
from ._server_info import (
    PROBES,
    ProbeEntry,
    ServerStatus,
    check_minimum,
    check_operation,
    server_status,
)
from .errors import (
    AuthenticationError,
    FrameWorksError,
    GraphQLError,
    HTTPError,
    NetworkError,
    PaymentRequiredError,
    ProtocolError,
    RateLimitError,
    SchemaMismatchError,
    ServerError,
    ServerTooOldError,
    UnsupportedOperationError,
)
from .retry import (
    DEFAULT_RETRY_POLICY,
    RETRYABLE_STATUSES,
    RetryPolicy,
    backoff_delay_ms,
    parse_retry_after,
)

OperationKind = Literal["query", "mutation", "subscription"]

DEFAULT_GRAPHQL_URL = "https://bridge.frameworks.network/graphql"

#: A bearer token, or a function returning the current one. None sends no Authorization header.
Token = Union[str, None, Callable[[], Union[str, None]]]
#: As Token; the async client also accepts a coroutine function.
AsyncToken = Union[str, None, Callable[[], Union[str, None]], Callable[[], Awaitable[Union[str, None]]]]

SERVER_INFO_QUERY = "query ServerInfo {\n  serverInfo {\n    version\n    features\n  }\n}\n"


def parse_operation(query: str, operation_name: str | None = None) -> tuple[OperationKind, str | None]:
    operation = get_operation_ast(parse(query), operation_name)
    if operation is None:
        raise ValueError("document must select exactly one GraphQL operation")
    kind: OperationKind = "query"
    if operation.operation.value == "mutation":
        kind = "mutation"
    elif operation.operation.value == "subscription":
        kind = "subscription"
    return kind, operation.name.value if operation.name else None


@dataclass(frozen=True)
class GraphQLResponse:
    """The data of a successful operation, as execute returns it to the generated method."""

    data: dict[str, Any]


@dataclass(frozen=True)
class _Outcome:
    """One attempt. retryable says a query may be sent again; unsent says the
    request never reached the server, so a mutation may be sent again too."""

    data: dict[str, Any] | None = None
    error: FrameWorksError | None = None
    retryable: bool = False
    unsent: bool = False


def graphql_error(
    errors: list[Mapping[str, Any]],
    data: Any,
    status: int | None,
    retry_after_seconds: int | None,
    body: Any,
) -> FrameWorksError:
    """Maps GraphQL errors by the extensions.code of the first entry."""
    first = errors[0] if errors else {}
    extensions = first.get("extensions")
    raw_code = extensions.get("code") if isinstance(extensions, Mapping) else None
    code = raw_code if isinstance(raw_code, str) else None
    message = first.get("message")
    text = message if isinstance(message, str) else "GraphQL error"
    details: dict[str, Any] = {
        "status": status,
        "code": code,
        "retry_after_seconds": retry_after_seconds,
        "body": body,
    }
    if code == "UNAUTHORIZED":
        return AuthenticationError(text, **details)
    if code == "RATE_LIMITED":
        return RateLimitError(text, **details)
    if code in ("GRAPHQL_VALIDATION_FAILED", "GRAPHQL_PARSE_FAILED"):
        return SchemaMismatchError(text, errors, data, **details)
    return GraphQLError(text, errors, data, **details)


def _http_error(
    status: int,
    obj: Mapping[str, Any] | None,
    text: str,
    retry_after: int | None,
    body: Any,
) -> FrameWorksError:
    code = obj.get("code") if obj else None
    message: str | None = None
    if obj:
        for key in ("message", "error"):
            value = obj.get(key)
            if isinstance(value, str) and value:
                message = value
                break
    if message is None:
        message = text.strip()[:200] or f"HTTP {status}"
    details: dict[str, Any] = {
        "status": status,
        "code": code if isinstance(code, str) else None,
        "retry_after_seconds": retry_after,
        "body": body if body is not None else text,
    }
    if status == 401:
        return AuthenticationError(message, **details)
    if status == 402:
        return PaymentRequiredError(message, **details)
    if status == 429:
        return RateLimitError(message, **details)
    if status >= 500:
        return ServerError(message, **details)
    return HTTPError(message, **details)


def classify(status: int, retry_after_header: str | None, text: str) -> _Outcome:
    """Turns one HTTP response into data or a typed error. A JSON body with
    an errors array is a GraphQL response at any status; otherwise a non-2xx
    status is an HTTP error."""
    retry_after = parse_retry_after(retry_after_header)
    retryable = status in RETRYABLE_STATUSES
    is_2xx = 200 <= status < 300
    body: Any = None
    try:
        body = json.loads(text) if text else None
    except ValueError:
        body = None
    obj = body if isinstance(body, dict) else None

    if obj is not None and isinstance(obj.get("errors"), list) and obj["errors"]:
        errors = [e for e in obj["errors"] if isinstance(e, Mapping)]
        return _Outcome(
            error=graphql_error(errors, obj.get("data"), None if is_2xx else status, retry_after, body),
            retryable=retryable,
        )
    if not is_2xx:
        return _Outcome(error=_http_error(status, obj, text, retry_after, body), retryable=retryable)
    if obj is None:
        return _Outcome(error=ProtocolError(f"response is not JSON (HTTP {status})", status=status, body=text))
    data = obj.get("data")
    if not isinstance(data, dict):
        return _Outcome(error=ProtocolError(f"response has no data (HTTP {status})", status=status, body=body))
    return _Outcome(data=data)


def _json_variables(variables: Mapping[str, Any] | None) -> dict[str, Any]:
    """Drops unset arguments and renders models and datetimes as JSON values."""
    from ._generated.graphql.base_model import UnsetType

    def convert(value: Any) -> Any:
        if isinstance(value, PydanticBaseModel):
            return value.model_dump(by_alias=True, exclude_unset=True)
        if isinstance(value, list):
            return [convert(item) for item in value]
        return value

    out = {k: convert(v) for k, v in (variables or {}).items() if not isinstance(v, UnsetType)}
    result: dict[str, Any] = to_jsonable_python(out)
    return result


@dataclass(frozen=True)
class _Call:
    kind: OperationKind
    name: str | None
    body: dict[str, Any]
    headers: dict[str, str]
    idempotency_key: str | None


class _TransportCore:
    """State and rules shared by the sync and async transports."""

    def __init__(
        self,
        url: str,
        *,
        headers: Mapping[str, str] | None,
        retry: RetryPolicy | None,
        check_server: bool,
        _operation_since: Mapping[str, str] | None,
    ) -> None:
        if not url:
            raise TypeError("url is required")
        self.url = url
        self.headers = dict(headers or {})
        self.retry = retry or DEFAULT_RETRY_POLICY
        self.check_server = check_server
        self._operation_since = dict(_operation_since or {})
        # The clock the serverInfo cache ages its answers against; tests
        # replace it.
        self._clock: Callable[[], float] = time.monotonic

    def _since(self, name: str | None) -> str | None:
        if name is None:
            return None
        if name in self._operation_since:
            return self._operation_since[name]
        info = OPERATIONS.get(name)
        return info["since"] if info else None

    def _call(
        self,
        query: str,
        operation_name: str | None,
        variables: Mapping[str, Any] | None,
        idempotency_key: str | None,
        playback_token: str | None,
        headers: Mapping[str, str] | None,
    ) -> _Call:
        kind, name = parse_operation(query, operation_name)
        body: dict[str, Any] = {"query": query, "variables": _json_variables(variables)}
        if name:
            body["operationName"] = name
        call_headers = {
            "content-type": "application/json",
            "accept": "application/graphql-response+json, application/json",
            **self.headers,
            **(headers or {}),
        }
        if playback_token:
            call_headers["x-frameworks-playback-jwt"] = playback_token
        if idempotency_key:
            call_headers["idempotency-key"] = idempotency_key
        return _Call(kind, name, body, call_headers, idempotency_key)

    def _check(self, status: ServerStatus, name: str | None) -> None:
        check_minimum(status, MIN_SERVER_VERSION)
        if name:
            check_operation(status, name, self._since(name))

    def _next_delay(self, outcome: _Outcome, call: _Call, attempt: int) -> int | None:
        """The wait before the next attempt, or None when the error is final.

        Queries retry network errors and 408, 429, and 5xx. A mutation, with
        or without an idempotency key, retries only when the request provably
        never reached the server: the connection could not be established, or
        the gateway answered 429. The gateway does not deduplicate replayed
        mutations, so a mutation is never sent again after a timeout, a
        dropped connection, or a 5xx."""
        assert outcome.error is not None
        if call.kind == "query":
            may_retry = outcome.retryable
        else:
            may_retry = outcome.unsent or outcome.error.status == 429
        if not may_retry or attempt >= self.retry.max_attempts:
            return None
        retry_after = outcome.error.retry_after_seconds
        if retry_after is not None:
            if retry_after * 1000 > self.retry.max_retry_after_ms:
                return None
            return retry_after * 1000
        return backoff_delay_ms(self.retry, attempt)

    @staticmethod
    def _authorization(headers: dict[str, str], token: str | None) -> dict[str, str]:
        if token:
            return {**headers, "authorization": f"Bearer {token}"}
        return headers

    @staticmethod
    def _probe_status(outcome: _Outcome) -> ServerStatus:
        assert outcome.data is not None
        info = outcome.data.get("serverInfo")
        if not isinstance(info, dict) or not isinstance(info.get("version"), str):
            raise ProtocolError("serverInfo response has no version")
        features = info.get("features")
        return server_status(
            info["version"],
            [f for f in features if isinstance(f, str)] if isinstance(features, list) else [],
        )


_SyncT = TypeVar("_SyncT", bound="SyncTransport")
_AsyncT = TypeVar("_AsyncT", bound="AsyncTransport")


def _cached_status(entry: ProbeEntry) -> ServerStatus:
    """The cached answer, or the cached 402 raised again."""
    if entry.payment is not None:
        raise entry.payment
    assert entry.status is not None
    return entry.status


def _network_error(url: str, err: BaseException) -> _Outcome:
    # httpx raises ConnectError only while establishing the connection (a
    # refused connection, a failed DNS lookup, a failed TLS handshake), before
    # any byte of the request is sent. ConnectTimeout is not a ConnectError.
    unsent = isinstance(err, httpx.ConnectError)
    return _Outcome(
        error=NetworkError(f"request to {url} failed: {err}"),
        retryable=True,
        unsent=unsent,
    )


class SyncTransport(_TransportCore):
    """Sync transport of the generated GraphQLClient.

    token is a bearer token or a function returning the current one; it is
    read for every attempt. The client never reads the environment.
    check_server=False skips the serverInfo gate, so the client never raises
    ServerTooOldError or UnsupportedOperationError.
    """

    def __init__(
        self,
        url: str = DEFAULT_GRAPHQL_URL,
        *,
        token: Token = None,
        http_client: httpx.Client | None = None,
        headers: Mapping[str, str] | None = None,
        retry: RetryPolicy | None = None,
        check_server: bool = True,
        _sleep: Callable[[float], None] | None = None,
        _operation_since: Mapping[str, str] | None = None,
    ) -> None:
        super().__init__(
            url,
            headers=headers,
            retry=retry,
            check_server=check_server,
            _operation_since=_operation_since,
        )
        self.token = token
        self.http_client = http_client or httpx.Client()
        self._sleep = _sleep or time.sleep

    def __enter__(self: _SyncT) -> _SyncT:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def close(self) -> None:
        self.http_client.close()

    def _token(self) -> str | None:
        value = self.token() if callable(self.token) else self.token
        return value or None

    def _attempt(self, call: _Call) -> _Outcome:
        try:
            response = self.http_client.post(
                self.url,
                json=call.body,
                headers=self._authorization(call.headers, self._token()),
            )
            text = response.text
        except httpx.TransportError as err:
            return _network_error(self.url, err)
        return classify(response.status_code, response.headers.get("retry-after"), text)

    def _send(self, call: _Call) -> dict[str, Any]:
        attempt = 1
        while True:
            outcome = self._attempt(call)
            if outcome.data is not None:
                return outcome.data
            delay = self._next_delay(outcome, call, attempt)
            assert outcome.error is not None
            if delay is None:
                raise outcome.error
            self._sleep(delay / 1000)
            attempt += 1

    def server_status(self) -> ServerStatus:
        """The server's version and features, from the process-wide serverInfo
        probe of this URL, reused for five minutes. (server_info is the
        generated ServerInfo query.)"""
        return self._status(refresh=False)[0]

    def _status(self, *, refresh: bool) -> tuple[ServerStatus, bool]:
        """The probe answer and whether it was reused from the cache; refresh
        asks the server even when a cached answer is still fresh."""
        if not refresh:
            cached = PROBES.get(self.url, self._clock())
            if cached is not None:
                return _cached_status(cached), True
        with PROBES.url_lock(self.url):
            if not refresh:
                cached = PROBES.get(self.url, self._clock())
                if cached is not None:
                    return _cached_status(cached), True
            call = self._call(SERVER_INFO_QUERY, "ServerInfo", None, None, None, None)
            try:
                status = self._probe_status(_Outcome(data=self._send(call)))
            except SchemaMismatchError:
                status = ServerStatus(None)
            except PaymentRequiredError as err:
                PROBES.put(self.url, ProbeEntry(None, err, self._clock()))
                raise
            PROBES.put(self.url, ProbeEntry(status, None, self._clock()))
            return status, False

    def _gate(self, name: str | None) -> None:
        # A failed probe (or a 402) is not a verdict: the operation proceeds
        # and its own response decides the outcome. A cached answer that
        # would fail the call is asked again first, so an upgraded server is
        # seen at once.
        try:
            status, cached = self._status(refresh=False)
        except FrameWorksError:
            return
        try:
            self._check(status, name)
        except (ServerTooOldError, UnsupportedOperationError):
            if not cached:
                raise
            try:
                status, _ = self._status(refresh=True)
            except FrameWorksError:
                return
            self._check(status, name)

    def _recheck(self, name: str | None, mismatch: SchemaMismatchError) -> None:
        """An operation the server rejected as invalid against its schema may
        mean the server changed since the cached probe: asks again at once
        and raises the new verdict when it fails the check, else mismatch."""
        try:
            status, _ = self._status(refresh=True)
        except FrameWorksError:
            raise mismatch from None
        self._check(status, name)
        raise mismatch

    def execute(
        self,
        query: str,
        operation_name: str | None = None,
        variables: dict[str, Any] | None = None,
        *,
        idempotency_key: str | None = None,
        playback_token: str | None = None,
        headers: Mapping[str, str] | None = None,
        **_: Any,
    ) -> GraphQLResponse:
        """Runs one query or mutation. idempotency_key is sent as
        Idempotency-Key (paid mutations settled with x402 need one); a
        mutation is retried only when the request never reached the server;
        playback_token is sent as X-Frameworks-Playback-JWT."""
        call = self._call(query, operation_name, variables, idempotency_key, playback_token, headers)
        if call.kind == "subscription":
            raise TypeError("subscriptions run over WebSocket; use AsyncFrameWorksClient")
        if self.check_server and call.name != "ServerInfo":
            self._gate(call.name)
        try:
            return GraphQLResponse(self._send(call))
        except SchemaMismatchError as err:
            if self.check_server and call.name != "ServerInfo":
                self._recheck(call.name, err)
            raise

    def get_data(self, response: GraphQLResponse) -> dict[str, Any]:
        return response.data


class AsyncTransport(_TransportCore):
    """Async transport of the generated AsyncGraphQLClient, including
    subscriptions over WebSocket (graphql-transport-ws).

    ws_url is the WebSocket endpoint; it defaults to url with ws(s) and /ws
    appended. max_reconnects bounds the reconnects of a subscription in a row
    without an acknowledged connection between them.
    """

    _probe_locks: dict[tuple[int, str], asyncio.Lock] = {}

    def __init__(
        self,
        url: str = DEFAULT_GRAPHQL_URL,
        *,
        token: AsyncToken = None,
        http_client: httpx.AsyncClient | None = None,
        headers: Mapping[str, str] | None = None,
        retry: RetryPolicy | None = None,
        check_server: bool = True,
        ws_url: str | None = None,
        max_reconnects: int = 5,
        _sleep: Callable[[float], Awaitable[None]] | None = None,
        _operation_since: Mapping[str, str] | None = None,
    ) -> None:
        super().__init__(
            url,
            headers=headers,
            retry=retry,
            check_server=check_server,
            _operation_since=_operation_since,
        )
        self.token = token
        self.http_client = http_client or httpx.AsyncClient()
        self.ws_url = ws_url or _default_ws_url(url)
        self.max_reconnects = max_reconnects
        self._sleep = _sleep or asyncio.sleep

    async def __aenter__(self: _AsyncT) -> _AsyncT:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self.http_client.aclose()

    async def _token(self) -> str | None:
        value: Any = self.token() if callable(self.token) else self.token
        if inspect.isawaitable(value):
            value = await value
        return value if isinstance(value, str) and value else None

    async def _attempt(self, call: _Call) -> _Outcome:
        try:
            response = await self.http_client.post(
                self.url,
                json=call.body,
                headers=self._authorization(call.headers, await self._token()),
            )
            text = response.text
        except httpx.TransportError as err:
            return _network_error(self.url, err)
        return classify(response.status_code, response.headers.get("retry-after"), text)

    async def _send(self, call: _Call) -> dict[str, Any]:
        attempt = 1
        while True:
            outcome = await self._attempt(call)
            if outcome.data is not None:
                return outcome.data
            delay = self._next_delay(outcome, call, attempt)
            assert outcome.error is not None
            if delay is None:
                raise outcome.error
            await self._sleep(delay / 1000)
            attempt += 1

    async def server_status(self) -> ServerStatus:
        """The server's version and features, from the process-wide serverInfo
        probe of this URL, reused for five minutes. (server_info is the
        generated ServerInfo query.)"""
        return (await self._status(refresh=False))[0]

    async def _status(self, *, refresh: bool) -> tuple[ServerStatus, bool]:
        """See SyncTransport._status."""
        if not refresh:
            cached = PROBES.get(self.url, self._clock())
            if cached is not None:
                return _cached_status(cached), True
        key = (id(asyncio.get_running_loop()), self.url)
        lock = self._probe_locks.setdefault(key, asyncio.Lock())
        async with lock:
            if not refresh:
                cached = PROBES.get(self.url, self._clock())
                if cached is not None:
                    return _cached_status(cached), True
            call = self._call(SERVER_INFO_QUERY, "ServerInfo", None, None, None, None)
            try:
                status = self._probe_status(_Outcome(data=await self._send(call)))
            except SchemaMismatchError:
                status = ServerStatus(None)
            except PaymentRequiredError as err:
                PROBES.put(self.url, ProbeEntry(None, err, self._clock()))
                raise
            PROBES.put(self.url, ProbeEntry(status, None, self._clock()))
            return status, False

    async def _gate(self, name: str | None) -> None:
        """See SyncTransport._gate."""
        try:
            status, cached = await self._status(refresh=False)
        except FrameWorksError:
            return
        try:
            self._check(status, name)
        except (ServerTooOldError, UnsupportedOperationError):
            if not cached:
                raise
            try:
                status, _ = await self._status(refresh=True)
            except FrameWorksError:
                return
            self._check(status, name)

    async def _recheck(self, name: str | None, mismatch: SchemaMismatchError) -> None:
        """See SyncTransport._recheck."""
        try:
            status, _ = await self._status(refresh=True)
        except FrameWorksError:
            raise mismatch from None
        self._check(status, name)
        raise mismatch

    async def execute(
        self,
        query: str,
        operation_name: str | None = None,
        variables: dict[str, Any] | None = None,
        *,
        idempotency_key: str | None = None,
        playback_token: str | None = None,
        headers: Mapping[str, str] | None = None,
        **_: Any,
    ) -> GraphQLResponse:
        """Runs one query or mutation; see SyncTransport.execute."""
        call = self._call(query, operation_name, variables, idempotency_key, playback_token, headers)
        if call.kind == "subscription":
            raise TypeError("subscriptions use the generated async subscription methods")
        if self.check_server and call.name != "ServerInfo":
            await self._gate(call.name)
        try:
            return GraphQLResponse(await self._send(call))
        except SchemaMismatchError as err:
            if self.check_server and call.name != "ServerInfo":
                await self._recheck(call.name, err)
            raise

    def get_data(self, response: GraphQLResponse) -> dict[str, Any]:
        return response.data

    async def execute_ws(
        self,
        query: str,
        operation_name: str | None = None,
        variables: dict[str, Any] | None = None,
        **_: Any,
    ) -> AsyncIterator[dict[str, Any]]:
        """Subscribes over graphql-transport-ws and yields each event's data.

        The token is read again for every connection. A connection the
        server closes with 4403 before connection_ack (the gateway's answer
        to an invalid or expired token) raises AuthenticationError and is not
        retried. Any other close or connection failure, before or after the
        ack, reconnects and subscribes again; max_reconnects bounds the
        reconnects in a row without an acknowledged connection between them.
        An error message raises the mapped GraphQL error; complete ends the
        iteration.
        """
        from websockets.asyncio.client import connect
        from websockets.exceptions import ConnectionClosed, InvalidHandshake

        name = parse_operation(query, operation_name)[1]
        if self.check_server:
            await self._gate(name)
        payload: dict[str, Any] = {
            "query": query,
            "variables": _json_variables(variables),
        }
        if name:
            payload["operationName"] = name
        reconnects = 0
        while True:
            acked = False
            failure: FrameWorksError
            try:
                async with connect(self.ws_url, subprotocols=[_subprotocol()], open_timeout=10) as ws:
                    token = await self._token()
                    init: dict[str, Any] = {"type": "connection_init", "payload": {}}
                    if token:
                        init["payload"] = {"Authorization": f"Bearer {token}"}
                    sub_id = str(uuid.uuid4())
                    try:
                        await ws.send(json.dumps(init))
                        async for raw in ws:
                            message = json.loads(raw)
                            mtype = message.get("type")
                            if mtype == "ping":
                                await ws.send(json.dumps({"type": "pong"}))
                                continue
                            if not acked:
                                if mtype != "connection_ack":
                                    raise ProtocolError(f"first subscription message is {mtype}, not connection_ack")
                                acked = True
                                await ws.send(
                                    json.dumps(
                                        {
                                            "id": sub_id,
                                            "type": "subscribe",
                                            "payload": payload,
                                        }
                                    )
                                )
                                continue
                            if message.get("id") != sub_id:
                                continue
                            if mtype == "next":
                                result = message.get("payload") or {}
                                errors = result.get("errors")
                                if isinstance(errors, list) and errors:
                                    raise graphql_error(errors, result.get("data"), None, None, result)
                                yield result.get("data") or {}
                            elif mtype == "error":
                                errors = message.get("payload")
                                raise graphql_error(
                                    errors if isinstance(errors, list) else [],
                                    None,
                                    None,
                                    None,
                                    errors,
                                )
                            elif mtype == "complete":
                                return
                    except GeneratorExit:
                        if acked:
                            await ws.send(json.dumps({"id": sub_id, "type": "complete"}))
                        raise
                    except ConnectionClosed:
                        pass
                    # The server closed the socket without completing the subscription.
                    code, reason = ws.close_code or 1006, ws.close_reason or ""
                    if not acked and code == _CLOSE_FORBIDDEN:
                        raise AuthenticationError(
                            f"subscription connection closed before it was acknowledged ({code} {reason}); "
                            "the token is invalid or expired",
                            code=None,
                        )
                    failure = NetworkError(f"subscription connection closed ({code} {reason})")
            except (OSError, InvalidHandshake, asyncio.TimeoutError) as err:
                failure = NetworkError(f"subscription connection to {self.ws_url} failed: {err}")
            # max_reconnects bounds reconnects in a row without an
            # acknowledged connection between them.
            if acked:
                reconnects = 0
            if reconnects >= self.max_reconnects:
                raise failure
            reconnects += 1
            await self._sleep(
                backoff_delay_ms(
                    replace(self.retry, max_attempts=self.max_reconnects + 1),
                    reconnects,
                )
                / 1000
            )


#: graphql-transport-ws 4403 Forbidden: the gateway rejected the Authorization value.
_CLOSE_FORBIDDEN = 4403


def _subprotocol() -> Any:
    from websockets.typing import Subprotocol

    return Subprotocol("graphql-transport-ws")


def _default_ws_url(url: str) -> str:
    if url.startswith("https://"):
        base = "wss://" + url[len("https://") :]
    elif url.startswith("http://"):
        base = "ws://" + url[len("http://") :]
    else:
        base = url
    return base.rstrip("/") + "/ws"

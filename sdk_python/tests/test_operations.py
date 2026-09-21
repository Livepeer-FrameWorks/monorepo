"""sdk_conformance/operations.json: every generated operation, through the sync
and async clients, sends its variables and decodes a schema-shaped response."""

from __future__ import annotations

import inspect
import json
import typing
from typing import Any

import httpx
import pytest
from ariadne_codegen.utils import str_to_snake_case
from pydantic import BaseModel
from websockets.asyncio.server import ServerConnection, serve
from websockets.typing import Subprotocol

from conftest import load_fixture
from livepeer_frameworks import OPERATIONS, AsyncFrameWorksClient, FrameWorksClient

FIXTURE = load_fixture("operations.json")
QUERIES = [op for op in FIXTURE["operations"] if op["kind"] != "subscription"]
SUBSCRIPTIONS = [op for op in FIXTURE["operations"] if op["kind"] == "subscription"]


def test_fixture_covers_every_operation() -> None:
    assert sorted(op["name"] for op in FIXTURE["operations"]) == sorted(OPERATIONS)


def _model_class(annotation: Any) -> type[BaseModel] | None:
    for candidate in (annotation, *typing.get_args(annotation)):
        if isinstance(candidate, type) and issubclass(candidate, BaseModel):
            return candidate
    return None


def _arguments(method: Any, variables: dict[str, Any]) -> dict[str, Any]:
    """Maps fixture variables (camelCase) to the generated method's keyword arguments."""
    hints = typing.get_type_hints(method)
    params = inspect.signature(method).parameters
    out: dict[str, Any] = {}
    for name, value in variables.items():
        arg = str_to_snake_case(name)
        assert arg in params, f"{method.__name__} has no parameter {arg}"
        model = _model_class(hints.get(arg))
        out[arg] = model.model_validate(value) if model is not None else value
    return out


def _mock(op: dict[str, Any], sent: list[dict[str, Any]]) -> httpx.MockTransport:
    def handler(request: httpx.Request) -> httpx.Response:
        sent.append(json.loads(request.content))
        return httpx.Response(200, json=op["response"])

    return httpx.MockTransport(handler)


def _check(op: dict[str, Any], result: BaseModel, sent: list[dict[str, Any]]) -> None:
    assert result.model_dump(by_alias=True, mode="json") == op["response"]["data"]
    assert len(sent) == 1
    assert sent[0]["operationName"] == op["name"]
    assert sent[0]["variables"] == op["variables"]


@pytest.mark.parametrize("op", QUERIES, ids=lambda o: o["name"])
def test_operation_sync(op: dict[str, Any]) -> None:
    sent: list[dict[str, Any]] = []
    client = FrameWorksClient(
        "https://operations.test/graphql", http_client=httpx.Client(transport=_mock(op, sent)), check_server=False
    )
    method = getattr(client, str_to_snake_case(op["name"]))
    _check(op, method(**_arguments(method, op["variables"])), sent)


@pytest.mark.parametrize("op", QUERIES, ids=lambda o: o["name"])
async def test_operation_async(op: dict[str, Any]) -> None:
    sent: list[dict[str, Any]] = []
    client = AsyncFrameWorksClient(
        "https://operations.test/graphql", http_client=httpx.AsyncClient(transport=_mock(op, sent)), check_server=False
    )
    method = getattr(client, str_to_snake_case(op["name"]))
    _check(op, await method(**_arguments(method, op["variables"])), sent)


@pytest.mark.parametrize("op", SUBSCRIPTIONS, ids=lambda o: o["name"])
async def test_subscription_operation(op: dict[str, Any]) -> None:
    subscribed: list[dict[str, Any]] = []

    async def handler(ws: ServerConnection) -> None:
        async for raw in ws:
            msg = json.loads(raw)
            if msg["type"] == "connection_init":
                await ws.send(json.dumps({"type": "connection_ack"}))
            elif msg["type"] == "subscribe":
                subscribed.append(msg["payload"])
                await ws.send(json.dumps({"id": msg["id"], "type": "next", "payload": op["response"]}))
                await ws.send(json.dumps({"id": msg["id"], "type": "complete"}))

    async with serve(handler, "127.0.0.1", 0, subprotocols=[Subprotocol("graphql-transport-ws")]) as server:
        port = next(iter(server.sockets)).getsockname()[1]
        client = AsyncFrameWorksClient(
            "http://127.0.0.1/graphql", ws_url=f"ws://127.0.0.1:{port}", token="t", check_server=False
        )
        method = getattr(client, str_to_snake_case(op["name"]))
        events = [e async for e in method(**_arguments(method, op["variables"]))]
    assert [e.model_dump(by_alias=True, mode="json") for e in events] == [op["response"]["data"]]
    assert subscribed[0]["operationName"] == op["name"]

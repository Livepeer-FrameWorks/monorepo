"""sdk_conformance/subscriptions.json against a scripted local graphql-transport-ws server."""

from __future__ import annotations

import asyncio
import json
import socket
import struct
import typing
from typing import Any

import pytest
from ariadne_codegen.utils import str_to_snake_case
from pydantic import BaseModel
from websockets.asyncio.server import ServerConnection, serve
from websockets.typing import Subprotocol

from conftest import error_mismatches, load_fixture
from livepeer_frameworks import AsyncFrameWorksClient

FIXTURE = load_fixture("subscriptions.json")


async def _no_wait(_: float) -> None:
    return None


def _arguments(method: Any, variables: dict[str, Any]) -> dict[str, Any]:
    """Maps fixture variables (camelCase) to the generated method's keyword
    arguments, validating each input object into its pydantic model."""
    hints = typing.get_type_hints(method)
    out: dict[str, Any] = {}
    for name, value in variables.items():
        arg = str_to_snake_case(name)
        model = next(
            (c for c in (hints[arg], *typing.get_args(hints[arg])) if isinstance(c, type) and issubclass(c, BaseModel)),
            None,
        )
        out[arg] = model.model_validate(value) if model is not None else value
    return out


def _without_nulls(value: Any) -> Any:
    """Returns value with every null object member removed, at any depth."""
    if isinstance(value, dict):
        return {k: _without_nulls(v) for k, v in value.items() if v is not None}
    if isinstance(value, list):
        return [_without_nulls(v) for v in value]
    return value


@pytest.mark.parametrize("case", FIXTURE["cases"], ids=lambda c: c["name"])
async def test_subscription(case: dict[str, Any]) -> None:
    authorization: list[str | None] = []
    scripts = list(case["connections"])
    subscribed: list[dict[str, Any]] = []
    connections = 0

    async def handler(ws: ServerConnection) -> None:
        nonlocal connections
        script = scripts[connections] if connections < len(scripts) else None
        connections += 1
        async for raw in ws:
            msg = json.loads(raw)
            if msg["type"] == "connection_init":
                auth = (msg.get("payload") or {}).get("Authorization")
                authorization.append(auth if isinstance(auth, str) else None)
                if script is not None and script["onInit"] == "reset":
                    # Linger 0 makes the close send a TCP RST instead of a FIN.
                    sock = ws.transport.get_extra_info("socket")
                    sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
                    ws.transport.abort()
                    return
                if script is None or script["onInit"] == "close":
                    code = script.get("closeCode", 1000) if script else 1000
                    await ws.close(code, script.get("closeReason", "terminated") if script else "terminated")
                    return
                await ws.send(json.dumps({"type": "connection_ack"}))
            elif msg["type"] == "subscribe" and script is not None:
                payload = msg.get("payload") or {}
                subscribed.append({"operationName": payload.get("operationName"), "variables": payload.get("variables")})
                for data in script.get("next", []):
                    await ws.send(json.dumps({"id": msg["id"], "type": "next", "payload": {"data": data}}))
                if script.get("error"):
                    await ws.send(json.dumps({"id": msg["id"], "type": "error", "payload": script["error"]}))
                elif script.get("then") == "complete":
                    await ws.send(json.dumps({"id": msg["id"], "type": "complete"}))
                elif script.get("then") == "drop":
                    await asyncio.sleep(0.02)
                    await ws.close(script.get("dropCode", 1001), "going away")
                    return

    async with serve(handler, "127.0.0.1", 0, subprotocols=[Subprotocol("graphql-transport-ws")]) as server:
        port = next(iter(server.sockets)).getsockname()[1]
        tokens = list(case["tokens"])
        client = AsyncFrameWorksClient(
            "http://127.0.0.1/graphql",
            ws_url=f"ws://127.0.0.1:{port}",
            token=lambda: tokens.pop(0) if tokens else None,
            check_server=False,
            max_reconnects=FIXTURE["maxReconnects"],
            _sleep=_no_wait,
        )
        method = getattr(client, str_to_snake_case(case.get("operation", "TenantEvents")))
        variables = _arguments(method, case.get("variables", {}))
        events: list[BaseModel] = []
        error: BaseException | None = None
        try:
            async for event in method(**variables):
                events.append(event)
        except Exception as err:  # noqa: BLE001
            error = err
    expect = case["expect"]
    if "eventIds" in expect:
        assert [e.tenant_events.id for e in events] == expect["eventIds"]  # type: ignore[attr-defined]
    if "events" in expect:
        assert [e.model_dump(by_alias=True, mode="json") for e in events] == expect["events"]
    if "subscribed" in expect:
        assert [_without_nulls(s) for s in subscribed] == expect["subscribed"]
    assert connections == expect["connections"]
    assert authorization == expect["authorization"]
    if "error" in expect:
        assert error_mismatches(error, expect["error"]) == []
    else:
        assert error is None, repr(error)

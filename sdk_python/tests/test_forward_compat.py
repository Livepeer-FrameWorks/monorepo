"""sdk_conformance/forward_compat.json: a union member or enum value a newer
server adds decodes, through the sync and async clients, instead of failing."""

from __future__ import annotations

import json
from typing import Any

import httpx
import pytest
from ariadne_codegen.utils import str_to_snake_case
from pydantic import BaseModel

from conftest import error_mismatches, load_fixture
from livepeer_frameworks import (
    AsyncFrameWorksClient,
    FrameWorksClient,
    FrameWorksError,
    OpenEnum,
    UnknownMember,
    expect_result,
)
from test_operations import _arguments

CASES = load_fixture("forward_compat.json")["cases"]


def _mock(case: dict[str, Any]) -> httpx.MockTransport:
    return httpx.MockTransport(lambda _request: httpx.Response(200, json=case["response"]))


def _at(value: Any, path: list[str]) -> Any:
    for name in path:
        value = getattr(value, str_to_snake_case(name))
    return value


def _check(case: dict[str, Any], result: BaseModel) -> None:
    assert result.model_dump(by_alias=True, mode="json") == case["response"]["data"]
    if unknown := case.get("unknownMember"):
        member = _at(result, unknown["path"])
        assert isinstance(member, UnknownMember), f"decoded as {type(member).__name__}"
        assert member.typename__ == unknown["typename"]
        assert member.raw == unknown["raw"]
    if enum := case.get("enumValue"):
        value = _at(result, enum["path"])
        assert isinstance(value, OpenEnum)
        assert value == enum["value"]
        assert value.value == enum["value"]
        assert value.is_unknown
    if expect := case.get("expectResult"):
        field = _at(result, [expect["field"]])
        if "error" in case:
            with pytest.raises(FrameWorksError) as info:
                expect_result(field, *expect["success"])
            assert error_mismatches(info.value, case["error"]) == []
        else:
            assert expect_result(field, *expect["success"]) is field


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
def test_forward_compat_sync(case: dict[str, Any]) -> None:
    client = FrameWorksClient(
        "https://forward.test/graphql", http_client=httpx.Client(transport=_mock(case)), check_server=False
    )
    method = getattr(client, str_to_snake_case(case["operation"]))
    _check(case, method(**_arguments(method, case["variables"])))


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
async def test_forward_compat_async(case: dict[str, Any]) -> None:
    client = AsyncFrameWorksClient(
        "https://forward.test/graphql", http_client=httpx.AsyncClient(transport=_mock(case)), check_server=False
    )
    method = getattr(client, str_to_snake_case(case["operation"]))
    _check(case, await method(**_arguments(method, case["variables"])))


def test_known_enum_value_is_the_declared_member() -> None:
    from livepeer_frameworks.graphql import IngestMode

    assert IngestMode("PUSH") is IngestMode.PUSH
    assert not IngestMode.PUSH.is_unknown
    assert json.dumps(IngestMode("RELAYED")) == '"RELAYED"'

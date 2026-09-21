"""Operation selection must determine the retry policy of the operation sent."""

from typing import Any

import pytest

from conftest import Scripted, load_fixture
from livepeer_frameworks import AsyncFrameWorksClient, FrameWorksClient
from livepeer_frameworks._transport import parse_operation

CASES = load_fixture("operation-selection.json")["cases"]


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_parse_selection(case: dict[str, Any]) -> None:
    if case.get("invalid"):
        with pytest.raises(Exception):
            parse_operation(case["document"], case.get("operationName"))
    else:
        assert parse_operation(case["document"], case.get("operationName")) == (
            case["kind"],
            case["selectedName"],
        )


def check_attempts(case: dict[str, Any], scripted: Scripted) -> None:
    attempts = 0 if case.get("invalid") or case["kind"] == "subscription" else (3 if case["kind"] == "query" else 1)
    assert len(scripted.requests) == attempts
    if attempts:
        assert scripted.requests[0][0] == case["selectedName"]


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_selection_retry_sync(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": [{"status": 503}] * 3})
    with FrameWorksClient(
        "https://selection.test/graphql",
        http_client=scripted.sync_client(),
        check_server=False,
        _sleep=lambda _: None,
    ) as client:
        with pytest.raises(Exception):
            client.execute(case["document"], operation_name=case.get("operationName"))
    check_attempts(case, scripted)


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
async def test_selection_retry_async(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": [{"status": 503}] * 3})

    async def sleep(_: float) -> None:
        pass

    async with AsyncFrameWorksClient(
        "https://selection.test/graphql",
        http_client=scripted.async_client(),
        check_server=False,
        _sleep=sleep,
    ) as client:
        with pytest.raises(Exception):
            await client.execute(case["document"], operation_name=case.get("operationName"))
    check_attempts(case, scripted)

"""sdk_conformance/retry.json and errors.json against the sync and async clients."""

from __future__ import annotations

from typing import Any

import pytest

from conftest import Scripted, error_mismatches, load_fixture
from livepeer_frameworks import AsyncFrameWorksClient, FrameWorksClient, RetryPolicy, expect_result

RETRY = load_fixture("retry.json")
ERRORS = load_fixture("errors.json")
POLICY = RetryPolicy(
    max_attempts=RETRY["policy"]["maxAttempts"],
    base_delay_ms=RETRY["policy"]["baseDelayMs"],
    max_delay_ms=RETRY["policy"]["maxDelayMs"],
    max_retry_after_ms=RETRY["policy"]["maxRetryAfterMs"],
    jitter=RETRY["policy"]["jitter"],
)


def _check_retry(case: dict[str, Any], outcome: Any, scripted: Scripted, delays: list[float]) -> None:
    expect = case["expect"]
    if "error" in expect:
        assert isinstance(outcome, BaseException), f"call succeeded with {outcome!r}"
        assert error_mismatches(outcome, expect["error"]) == []
    else:
        assert outcome == expect["data"]
    assert len(scripted.requests) == expect["attempts"]
    assert [round(d * 1000) for d in delays] == expect["delaysMs"]
    if "idempotencyKeys" in expect:
        assert [h.get("idempotency-key") for _, h, _ in scripted.requests] == expect["idempotencyKeys"]


@pytest.mark.parametrize("case", RETRY["cases"], ids=lambda c: c["name"])
def test_retry_sync(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": case["responses"]})
    delays: list[float] = []
    client = FrameWorksClient(
        "https://retry.test/graphql",
        http_client=scripted.sync_client(),
        retry=POLICY,
        check_server=False,
        _sleep=delays.append,
    )
    outcome: Any
    try:
        outcome = client.get_data(
            client.execute(f"{case['kind']} RetryProbe {{ ok }}", variables={}, idempotency_key=case.get("idempotencyKey"))
        )
    except Exception as err:  # noqa: BLE001
        outcome = err
    _check_retry(case, outcome, scripted, delays)


@pytest.mark.parametrize("case", RETRY["cases"], ids=lambda c: c["name"])
async def test_retry_async(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": case["responses"]})
    delays: list[float] = []

    async def sleep(seconds: float) -> None:
        delays.append(seconds)

    client = AsyncFrameWorksClient(
        "https://retry.test/graphql",
        http_client=scripted.async_client(),
        retry=POLICY,
        check_server=False,
        _sleep=sleep,
    )
    outcome: Any
    try:
        outcome = client.get_data(
            await client.execute(
                f"{case['kind']} RetryProbe {{ ok }}", variables={}, idempotency_key=case.get("idempotencyKey")
            )
        )
    except Exception as err:  # noqa: BLE001
        outcome = err
    _check_retry(case, outcome, scripted, delays)


def _check_error_case(case: dict[str, Any], data: Any, err: BaseException | None) -> None:
    if err is None and "expectResult" in case:
        try:
            data = expect_result(data[case["expectResult"]["field"]], *case["expectResult"]["success"])
        except Exception as result_err:  # noqa: BLE001
            err = result_err
    if "error" in case:
        assert err is not None, f"call succeeded with {data!r}"
        assert error_mismatches(err, case["error"]) == []
    else:
        assert err is None
        assert data == case["result"]


@pytest.mark.parametrize("case", ERRORS["cases"], ids=lambda c: c["name"])
def test_error_shapes_sync(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": [case["response"]]})
    client = FrameWorksClient(
        "https://errors.test/graphql",
        http_client=scripted.sync_client(),
        retry=RetryPolicy(max_attempts=1),
        check_server=False,
    )
    data: Any = None
    err: BaseException | None = None
    try:
        data = client.get_data(client.execute("query ErrorProbe { a }"))
    except Exception as e:  # noqa: BLE001
        err = e
    _check_error_case(case, data, err)


@pytest.mark.parametrize("case", ERRORS["cases"], ids=lambda c: c["name"])
async def test_error_shapes_async(case: dict[str, Any]) -> None:
    scripted = Scripted({"*": [case["response"]]})
    client = AsyncFrameWorksClient(
        "https://errors.test/graphql",
        http_client=scripted.async_client(),
        retry=RetryPolicy(max_attempts=1),
        check_server=False,
    )
    data: Any = None
    err: BaseException | None = None
    try:
        data = client.get_data(await client.execute("query ErrorProbe { a }"))
    except Exception as e:  # noqa: BLE001
        err = e
    _check_error_case(case, data, err)

"""sdk_conformance/server_info.json against the sync and async clients."""

from __future__ import annotations

import itertools
from typing import Any

import pytest

from conftest import Scripted, error_mismatches, load_fixture
from livepeer_frameworks import AsyncFrameWorksClient, FrameWorksClient, RetryPolicy

CASES = load_fixture("server_info.json")["cases"]
# The probe cache is per URL and process-wide, so every client gets its own URL.
_urls = (f"https://server-info-{n}.test/graphql" for n in itertools.count())


class _Clock:
    """A clock the test moves forward for {advanceMs} entries."""

    def __init__(self) -> None:
        self.now = 1_000_000.0

    def __call__(self) -> float:
        return self.now


def _check_calls(case: dict[str, Any], outcomes: list[BaseException | None], scripted: Scripted) -> None:
    for i, (want, got) in enumerate(zip(case["expect"]["calls"], outcomes)):
        if "error" in want:
            assert got is not None, f"call {i} succeeded, want {want['error']['kind']}"
            assert error_mismatches(got, want["error"]) == []
        else:
            assert got is None, f"call {i} failed: {got!r}"
    for name, count in case["expect"].get("sent", {}).items():
        assert scripted.sent(name) == count, name


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
def test_server_info_sync(case: dict[str, Any]) -> None:
    scripted = Scripted(case["responses"])
    client = FrameWorksClient(
        next(_urls),
        http_client=scripted.sync_client(),
        retry=RetryPolicy(max_attempts=1),
        _operation_since=case.get("operationSince"),
    )
    clock = _Clock()
    client._clock = clock
    outcomes: list[BaseException | None] = []
    for name in case["calls"]:
        if isinstance(name, dict):
            clock.now += name["advanceMs"] / 1000
            continue
        try:
            if name == "GetStream":
                client.get_stream(id="s1")
            else:
                client.delete_stream(id="s1")
            outcomes.append(None)
        except Exception as err:  # noqa: BLE001
            outcomes.append(err)
    _check_calls(case, outcomes, scripted)
    expect = case["expect"]
    if "verified" in expect or "serverVersion" in expect:
        status = client.server_status()
        if "verified" in expect:
            assert status.verified == expect["verified"]
        if "serverVersion" in expect:
            assert status.version == expect["serverVersion"]


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
async def test_server_info_async(case: dict[str, Any]) -> None:
    scripted = Scripted(case["responses"])
    client = AsyncFrameWorksClient(
        next(_urls),
        http_client=scripted.async_client(),
        retry=RetryPolicy(max_attempts=1),
        _operation_since=case.get("operationSince"),
    )
    clock = _Clock()
    client._clock = clock
    outcomes: list[BaseException | None] = []
    for name in case["calls"]:
        if isinstance(name, dict):
            clock.now += name["advanceMs"] / 1000
            continue
        try:
            if name == "GetStream":
                await client.get_stream(id="s1")
            else:
                await client.delete_stream(id="s1")
            outcomes.append(None)
        except Exception as err:  # noqa: BLE001
            outcomes.append(err)
    _check_calls(case, outcomes, scripted)
    expect = case["expect"]
    if "verified" in expect or "serverVersion" in expect:
        status = await client.server_status()
        if "verified" in expect:
            assert status.verified == expect["verified"]
        if "serverVersion" in expect:
            assert status.version == expect["serverVersion"]

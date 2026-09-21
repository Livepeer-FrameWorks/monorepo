"""sdk_conformance/pagination.json against the sync and async paginators."""

from __future__ import annotations

from collections.abc import Sequence
from typing import Any

import pytest

from conftest import load_fixture
from livepeer_frameworks import (
    apaginate_offset,
    apaginate_page_token,
    apaginate_relay,
    paginate_offset,
    paginate_page_token,
    paginate_relay,
)

CASES = load_fixture("pagination.json")["cases"]


def _pages(case: dict[str, Any]) -> tuple[list[Any], Any]:
    requests: list[Any] = []
    pages = list(case["pages"])

    def next_page(request: dict[str, Any]) -> Any:
        requests.append(dict(request))
        if not pages:
            raise AssertionError("paginator requested a page past the last one")
        return pages.pop(0)

    return requests, next_page


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
def test_pagination_sync(case: dict[str, Any]) -> None:
    requests, next_page = _pages(case)
    kw: dict[str, Any] = {"page_size": case["pageSize"], "max_items": case.get("maxItems")}
    if case["strategy"] == "relay":
        items = list(paginate_relay(next_page, **kw))
    elif case["strategy"] == "offset":
        items = list(paginate_offset(next_page, **kw))
    else:
        field = case.get("itemsField", "items")

        def token_page(request: dict[str, Any]) -> tuple[Sequence[Any], str | None]:
            page = next_page(request)
            return page[field], page.get("nextPageToken")

        items = list(paginate_page_token(token_page, **kw))
    assert items == case["expectItems"]
    assert requests == case["expectRequests"]


@pytest.mark.parametrize("case", CASES, ids=lambda c: c["name"])
async def test_pagination_async(case: dict[str, Any]) -> None:
    requests, next_page = _pages(case)
    kw: dict[str, Any] = {"page_size": case["pageSize"], "max_items": case.get("maxItems")}

    async def page(request: dict[str, Any]) -> Any:
        return next_page(request)

    if case["strategy"] == "relay":
        items = [i async for i in apaginate_relay(page, **kw)]
    elif case["strategy"] == "offset":
        items = [i async for i in apaginate_offset(page, **kw)]
    else:
        field = case.get("itemsField", "items")

        async def token_page(request: dict[str, Any]) -> tuple[Sequence[Any], str | None]:
            p = next_page(request)
            return p[field], p.get("nextPageToken")

        items = [i async for i in apaginate_page_token(token_page, **kw)]
    assert items == case["expectItems"]
    assert requests == case["expectRequests"]

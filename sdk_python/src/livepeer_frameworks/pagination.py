"""Iterators over the three pagination shapes of the API. Each takes a
function that fetches one page for a page request, so any operation with
that shape can be walked:

    for stream in paginate_relay(
        lambda page: fw.list_streams(page=ConnectionInput.model_validate(page)).streams_connection
    ):
        ...

Page requests are plain dicts named like the operation arguments: relay
{"first", "after"} (a ConnectionInput), offset {"first", "offset"} (fields of
StorageArtifactsInput), and page token {"pageSize", "pageToken"}. Pages may
be the generated models or dicts.
"""

from __future__ import annotations

from collections.abc import AsyncIterator, Awaitable, Callable, Iterator, Sequence
from typing import Any, TypeVar

T = TypeVar("T")
DEFAULT_PAGE_SIZE = 50


def _get(page: Any, *names: str) -> Any:
    for name in names:
        if isinstance(page, dict):
            if name in page:
                return page[name]
        elif hasattr(page, name):
            return getattr(page, name)
    return None


def _relay_step(page: Any) -> tuple[Sequence[Any], str | None]:
    """The rows of a relay page and the cursor to continue with (None to stop)."""
    nodes = _get(page, "nodes") or []
    info = _get(page, "page_info", "pageInfo")
    has_next = bool(_get(info, "has_next_page", "hasNextPage"))
    cursor = _get(info, "end_cursor", "endCursor")
    return nodes, (cursor if has_next and cursor and nodes else None)


def _offset_step(page: Any) -> tuple[Sequence[Any], bool]:
    nodes = _get(page, "nodes") or []
    return nodes, bool(_get(page, "has_next_page", "hasNextPage")) and len(nodes) > 0


def paginate_relay(
    fetch_page: Callable[[dict[str, Any]], Any], *, page_size: int = DEFAULT_PAGE_SIZE, max_items: int | None = None
) -> Iterator[Any]:
    """Walks a relay connection (first/after, pageInfo.endCursor), e.g. streamsConnection."""
    after: str | None = None
    yielded = 0
    while True:
        nodes, cursor = _relay_step(fetch_page({"first": page_size, "after": after}))
        for node in nodes:
            if max_items is not None and yielded >= max_items:
                return
            yield node
            yielded += 1
        if (max_items is not None and yielded >= max_items) or cursor is None:
            return
        after = cursor


def paginate_offset(
    fetch_page: Callable[[dict[str, Any]], Any], *, page_size: int = DEFAULT_PAGE_SIZE, max_items: int | None = None
) -> Iterator[Any]:
    """Walks an offset-paged list (first/offset, hasNextPage), e.g. storageArtifactsConnection."""
    offset = 0
    yielded = 0
    while True:
        nodes, more = _offset_step(fetch_page({"first": page_size, "offset": offset}))
        for node in nodes:
            if max_items is not None and yielded >= max_items:
                return
            yield node
            yielded += 1
        if (max_items is not None and yielded >= max_items) or not more:
            return
        offset += len(nodes)


def paginate_page_token(
    fetch_page: Callable[[dict[str, Any]], tuple[Sequence[T], str | None]],
    *,
    page_size: int = DEFAULT_PAGE_SIZE,
    max_items: int | None = None,
) -> Iterator[T]:
    """Walks a page-token list (pageSize/pageToken, nextPageToken), e.g.
    dvrChapters. fetch_page returns the page's items and its next token."""
    token: str | None = None
    yielded = 0
    while True:
        items, next_token = fetch_page({"pageSize": page_size, "pageToken": token})
        for item in items:
            if max_items is not None and yielded >= max_items:
                return
            yield item
            yielded += 1
        if (max_items is not None and yielded >= max_items) or not next_token or not items:
            return
        token = next_token


async def apaginate_relay(
    fetch_page: Callable[[dict[str, Any]], Awaitable[Any]],
    *,
    page_size: int = DEFAULT_PAGE_SIZE,
    max_items: int | None = None,
) -> AsyncIterator[Any]:
    """Async paginate_relay."""
    after: str | None = None
    yielded = 0
    while True:
        nodes, cursor = _relay_step(await fetch_page({"first": page_size, "after": after}))
        for node in nodes:
            if max_items is not None and yielded >= max_items:
                return
            yield node
            yielded += 1
        if (max_items is not None and yielded >= max_items) or cursor is None:
            return
        after = cursor


async def apaginate_offset(
    fetch_page: Callable[[dict[str, Any]], Awaitable[Any]],
    *,
    page_size: int = DEFAULT_PAGE_SIZE,
    max_items: int | None = None,
) -> AsyncIterator[Any]:
    """Async paginate_offset."""
    offset = 0
    yielded = 0
    while True:
        nodes, more = _offset_step(await fetch_page({"first": page_size, "offset": offset}))
        for node in nodes:
            if max_items is not None and yielded >= max_items:
                return
            yield node
            yielded += 1
        if (max_items is not None and yielded >= max_items) or not more:
            return
        offset += len(nodes)


async def apaginate_page_token(
    fetch_page: Callable[[dict[str, Any]], Awaitable[tuple[Sequence[T], str | None]]],
    *,
    page_size: int = DEFAULT_PAGE_SIZE,
    max_items: int | None = None,
) -> AsyncIterator[T]:
    """Async paginate_page_token."""
    token: str | None = None
    yielded = 0
    while True:
        items, next_token = await fetch_page({"pageSize": page_size, "pageToken": token})
        for item in items:
            if max_items is not None and yielded >= max_items:
                return
            yield item
            yielded += 1
        if (max_items is not None and yielded >= max_items) or not next_token or not items:
            return
        token = next_token

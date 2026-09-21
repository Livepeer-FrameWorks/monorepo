"""The hand-written Python surface hides generator naming and has useful defaults."""

from __future__ import annotations

import httpx

import livepeer_frameworks.graphql as graphql
from livepeer_frameworks import AsyncFrameWorksClient, DEFAULT_GRAPHQL_URL, FrameWorksClient, expect_result
from livepeer_frameworks._generated.graphql.create_stream import CreateStreamCreateStreamStream
from livepeer_frameworks._generated.graphql.tenant_events import TenantEventsTenantEventsDataStreamUpdated
from livepeer_frameworks.graphql import Stream


def test_client_defaults_to_hosted_bridge() -> None:
    with FrameWorksClient(http_client=httpx.Client(), check_server=False) as client:
        assert client.url == DEFAULT_GRAPHQL_URL
        assert client.url == "https://bridge.frameworks.network/graphql"


async def test_async_client_defaults_to_hosted_bridge_and_websocket() -> None:
    async with AsyncFrameWorksClient(http_client=httpx.AsyncClient(), check_server=False) as client:
        assert client.url == DEFAULT_GRAPHQL_URL
        assert client.ws_url == "wss://bridge.frameworks.network/graphql/ws"


def test_public_stream_model_hides_generated_operation_path() -> None:
    value = CreateStreamCreateStreamStream.model_construct(typename__="Stream")

    assert Stream.__name__ == "Stream"
    assert expect_result(value, Stream) is value
    assert "CreateStreamCreateStreamStream" not in graphql.__all__
    assert not hasattr(graphql, "CreateStreamCreateStreamStream")


def test_fragment_name_cleanup_does_not_change_wire_aliases() -> None:
    event = TenantEventsTenantEventsDataStreamUpdated.model_validate(
        {"__typename": "StreamUpdated", "streamId": "stream-1", "changedFields": ["name"]}
    )

    assert event.changed_fields == ["name"]
    assert event.model_dump(by_alias=True)["changedFields"] == ["name"]

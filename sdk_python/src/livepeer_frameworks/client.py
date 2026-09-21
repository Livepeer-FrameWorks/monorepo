"""The FrameWorks clients: the generated typed operations on the SDK transport."""

from __future__ import annotations

from ._generated.graphql.async_client import AsyncGraphQLClient
from ._generated.graphql.client import GraphQLClient


class FrameWorksClient(GraphQLClient):
    """Sync client. One method per public operation, e.g. create_stream,
    list_streams, delete_vod_asset; each returns the operation's pydantic
    model.

        fw = FrameWorksClient("https://bridge.example.com/graphql", token="...")

    token is a bearer token or a function returning the current one.
    Per-call keywords on every method: idempotency_key (sent as
    Idempotency-Key; it does not permit replay of a possibly executed mutation),
    playback_token (sent as X-Frameworks-Playback-JWT), and headers.
    server_status() returns the server's version as the serverInfo gate saw it.
    """


class AsyncFrameWorksClient(AsyncGraphQLClient):
    """Async client with the same methods as FrameWorksClient, plus the
    subscriptions (tenant_events), which run over WebSocket at ws_url
    (default: url with ws(s):// and /ws appended). token may also be an
    async function."""

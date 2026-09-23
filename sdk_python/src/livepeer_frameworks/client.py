"""The FrameWorks clients: generated typed operations on the SDK transport."""

from __future__ import annotations

from ._generated.graphql.async_client import AsyncGraphQLClient
from ._generated.graphql.client import GraphQLClient
from ._transport import DEFAULT_GRAPHQL_URL


class FrameWorksClient(GraphQLClient):
    """Sync client. One method per public operation, e.g. create_stream,
    list_streams, delete_vod_asset; each returns the operation's pydantic
    model.

        fw = FrameWorksClient(token="...")

    token is a bearer token or a function returning the current one.
    Per-call keywords on every method: idempotency_key (sent as
    Idempotency-Key; it does not permit replay of a possibly executed mutation),
    playback_token (sent as X-Frameworks-Playback-JWT), and headers.
    server_status() returns the server's version as the serverInfo gate saw it.
    url defaults to DEFAULT_GRAPHQL_URL; pass it explicitly for a self-hosted
    or staging Bridge.

    The sync client has no subscriptions: ariadne-codegen generates
    subscription methods only for an async client, so they are on
    AsyncFrameWorksClient.
    """


class AsyncFrameWorksClient(AsyncGraphQLClient):
    """Async client with the same methods as FrameWorksClient, plus one async
    iterator method per public subscription (tenant_events,
    live_stream_events, skipper_chat, ...), each yielding the operation's
    pydantic model. Subscriptions run over WebSocket at ws_url (default: url
    with ws(s):// and /ws appended). token may also be an async function.
    url defaults to DEFAULT_GRAPHQL_URL."""


__all__ = ["AsyncFrameWorksClient", "DEFAULT_GRAPHQL_URL", "FrameWorksClient"]

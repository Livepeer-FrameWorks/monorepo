from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PullSourceEventDefault


class GetRecentPullSourceEvents(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["GetRecentPullSourceEventsStream"] = Field(
        description="Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."
    )
    "Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class GetRecentPullSourceEventsStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    recent_pull_source_events: Optional[
        list["GetRecentPullSourceEventsStreamRecentPullSourceEvents"]
    ] = Field(
        alias="recentPullSourceEvents",
        description="Recent pull-source resolution events for pull streams. Captures the\ncustomer-facing resolution outcome (resolved, not_found, disabled,\nblocked_uri, cluster_not_allowed_delegate, commodore_error,\nfoghorn_base_unresolved). Null for push streams.",
    )
    "Recent pull-source resolution events for pull streams. Captures the\ncustomer-facing resolution outcome (resolved, not_found, disabled,\nblocked_uri, cluster_not_allowed_delegate, commodore_error,\nfoghorn_base_unresolved). Null for push streams."


class GetRecentPullSourceEventsStreamRecentPullSourceEvents(PullSourceEventDefault):
    """A single pull-source resolution outcome from Foghorn's STREAM_SOURCE
    handler. Append-only; written every time a pull+ stream's source is
    re-evaluated (typically on each Mist input start)."""

    pass


GetRecentPullSourceEvents.model_rebuild()
GetRecentPullSourceEventsStream.model_rebuild()

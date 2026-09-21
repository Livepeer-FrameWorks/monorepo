from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    PageInfo,
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
)


class ListStreams(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    streams_connection: "ListStreamsStreamsConnection" = Field(
        alias="streamsConnection",
        description="List all streams for the current tenant with pagination.",
    )
    "List all streams for the current tenant with pagination."


class ListStreamsStreamsConnection(BaseModel):
    nodes: list["ListStreamsStreamsConnectionNodes"]
    page_info: "ListStreamsStreamsConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListStreamsStreamsConnectionNodes(Stream):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    pass


ListStreamsStreamsConnectionPageInfo = PageInfo
ListStreams.model_rebuild()
ListStreamsStreamsConnection.model_rebuild()

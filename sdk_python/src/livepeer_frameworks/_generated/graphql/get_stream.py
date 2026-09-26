from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import Stream, StreamPlaybackPolicy, StreamPullSource  # noqa: F401


class GetStream(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["GetStreamStream"] = Field(
        description="Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."
    )
    "Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class GetStreamStream(Stream):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    pass


GetStream.model_rebuild()

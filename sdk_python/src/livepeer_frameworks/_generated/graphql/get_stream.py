from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
)


class GetStream(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["GetStreamStream"] = Field(
        description="Fetch a single stream by its global ID."
    )
    "Fetch a single stream by its global ID."


class GetStreamStream(Stream):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    pass


GetStream.model_rebuild()

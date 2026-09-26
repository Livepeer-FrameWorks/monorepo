from typing import Optional

from pydantic import Field

from .base_model import BaseModel


class GetStreamKey(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["GetStreamKeyStream"] = Field(
        description="Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."
    )
    "Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class GetStreamKeyStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    stream_key: Optional[str] = Field(
        alias="streamKey",
        description="Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path.",
    )
    "Secret key for publisher-authenticated ingest; null for pull and managed sources.\nThe key lets its holder publish to the stream, so an API token needs the\nstreams:write scope: without it this field is null and the response carries\na FORBIDDEN error at its path."


GetStreamKey.model_rebuild()

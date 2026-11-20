from datetime import datetime
from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import StreamIngestState
from .fragments import PageInfo


class ListStreamIngest(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    streams_connection: "ListStreamIngestStreamsConnection" = Field(
        alias="streamsConnection",
        description="List all streams for the current tenant with pagination.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write.",
    )
    "List all streams for the current tenant with pagination.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class ListStreamIngestStreamsConnection(BaseModel):
    nodes: list["ListStreamIngestStreamsConnectionNodes"]
    page_info: "ListStreamIngestStreamsConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListStreamIngestStreamsConnectionNodes(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    stream_id: str = Field(
        alias="streamId",
        description="Public stream UUID used for analytics and service APIs (not the Relay ID).",
    )
    "Public stream UUID used for analytics and service APIs (not the Relay ID)."
    ingest: Optional["ListStreamIngestStreamsConnectionNodesIngest"] = Field(
        description="Ingest state under streams:read; null with a field error when the owning cell is unavailable."
    )
    "Ingest state under streams:read; null with a field error when the owning cell is unavailable."


class ListStreamIngestStreamsConnectionNodesIngest(BaseModel):
    """Current ingest-session state, readable with streams:read."""

    state: StreamIngestState
    is_live: bool = Field(alias="isLive")
    session_id: Optional[str] = Field(alias="sessionId")
    connected_at: Optional[datetime] = Field(alias="connectedAt")
    live_since: Optional[datetime] = Field(alias="liveSince")
    ended_at: Optional[datetime] = Field(alias="endedAt")
    resumable_until: Optional[datetime] = Field(alias="resumableUntil")


ListStreamIngestStreamsConnectionPageInfo = PageInfo
ListStreamIngest.model_rebuild()
ListStreamIngestStreamsConnection.model_rebuild()
ListStreamIngestStreamsConnectionNodes.model_rebuild()

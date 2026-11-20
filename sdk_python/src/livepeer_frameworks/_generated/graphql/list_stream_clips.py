from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, StorageArtifact


class ListStreamClips(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["ListStreamClipsStream"] = Field(
        description="Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."
    )
    "Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class ListStreamClipsStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    clips: Optional["ListStreamClipsStreamClips"] = Field(
        description="Clips from this stream's tenant catalog, newest first."
    )
    "Clips from this stream's tenant catalog, newest first."


class ListStreamClipsStreamClips(BaseModel):
    """A keyset-paginated collection of one stream's saved recordings or clips."""

    edges: list["ListStreamClipsStreamClipsEdges"]
    page_info: "ListStreamClipsStreamClipsPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListStreamClipsStreamClipsEdges(BaseModel):
    cursor: str
    node: "ListStreamClipsStreamClipsEdgesNode"


ListStreamClipsStreamClipsEdgesNode = StorageArtifact
ListStreamClipsStreamClipsPageInfo = PageInfo
ListStreamClips.model_rebuild()
ListStreamClipsStream.model_rebuild()
ListStreamClipsStreamClips.model_rebuild()
ListStreamClipsStreamClipsEdges.model_rebuild()

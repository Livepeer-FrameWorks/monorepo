from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, StorageArtifact


class ListStreamRecordings(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["ListStreamRecordingsStream"] = Field(
        description="Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."
    )
    "Fetch a single stream by its global ID.\nAn API token needs the streams:read or streams:write scope. Stream.streamKey\nneeds streams:write."


class ListStreamRecordingsStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    recordings: Optional["ListStreamRecordingsStreamRecordings"] = Field(
        description="Recordings from this stream's tenant catalog, newest first."
    )
    "Recordings from this stream's tenant catalog, newest first."


class ListStreamRecordingsStreamRecordings(BaseModel):
    """A keyset-paginated collection of one stream's saved recordings or clips."""

    edges: list["ListStreamRecordingsStreamRecordingsEdges"]
    page_info: "ListStreamRecordingsStreamRecordingsPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


class ListStreamRecordingsStreamRecordingsEdges(BaseModel):
    cursor: str
    node: "ListStreamRecordingsStreamRecordingsEdgesNode"


ListStreamRecordingsStreamRecordingsEdgesNode = StorageArtifact
ListStreamRecordingsStreamRecordingsPageInfo = PageInfo
ListStreamRecordings.model_rebuild()
ListStreamRecordingsStream.model_rebuild()
ListStreamRecordingsStreamRecordings.model_rebuild()
ListStreamRecordingsStreamRecordingsEdges.model_rebuild()

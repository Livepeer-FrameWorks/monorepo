from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import DVRChapterRef, PageInfo


class ListRecordingChapters(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    dvr_recording: Optional["ListRecordingChaptersDvrRecording"] = Field(
        alias="dvrRecording",
        description="A recording from the tenant catalog, readable with streams:read.",
    )
    "A recording from the tenant catalog, readable with streams:read."


class ListRecordingChaptersDvrRecording(BaseModel):
    id: Optional[str]
    dvr_hash: str = Field(alias="dvrHash")
    chapters: Optional["ListRecordingChaptersDvrRecordingChapters"] = Field(
        description="Saved chapters, paged by their start time and ID. Range bounds are UTC epoch milliseconds."
    )
    "Saved chapters, paged by their start time and ID. Range bounds are UTC epoch milliseconds."


class ListRecordingChaptersDvrRecordingChapters(BaseModel):
    """A keyset-paginated view of a recording's saved chapters."""

    edges: list["ListRecordingChaptersDvrRecordingChaptersEdges"]
    page_info: "ListRecordingChaptersDvrRecordingChaptersPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class ListRecordingChaptersDvrRecordingChaptersEdges(BaseModel):
    cursor: str
    node: "ListRecordingChaptersDvrRecordingChaptersEdgesNode"


class ListRecordingChaptersDvrRecordingChaptersEdgesNode(DVRChapterRef):
    """A reference to a chapter for the chapter list UI. Same shape as
    DVRChapter without the timeline-zero derivations."""

    pass


ListRecordingChaptersDvrRecordingChaptersPageInfo = PageInfo
ListRecordingChapters.model_rebuild()
ListRecordingChaptersDvrRecording.model_rebuild()
ListRecordingChaptersDvrRecordingChapters.model_rebuild()
ListRecordingChaptersDvrRecordingChaptersEdges.model_rebuild()

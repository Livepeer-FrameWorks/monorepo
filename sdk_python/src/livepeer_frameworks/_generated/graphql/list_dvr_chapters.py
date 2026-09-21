from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import DVRChapterRef


class ListDVRChapters(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    dvr_chapters: Optional["ListDVRChaptersDvrChapters"] = Field(
        alias="dvrChapters",
        description="List chapters for a DVR recording. Paginated for unbounded artifact\nlifetime — default 200 per page, max 1000.",
    )
    "List chapters for a DVR recording. Paginated for unbounded artifact\nlifetime — default 200 per page, max 1000."


class ListDVRChaptersDvrChapters(BaseModel):
    """A page of DVR chapter refs. Paginated for unbounded artifact lifetime:
    default page size is 200 and maximum page size is 1000."""

    chapters: list["ListDVRChaptersDvrChaptersChapters"]
    next_page_token: Optional[str] = Field(alias="nextPageToken")


class ListDVRChaptersDvrChaptersChapters(DVRChapterRef):
    """A reference to a chapter for the chapter list UI. Same shape as
    DVRChapter without the timeline-zero derivations."""

    pass


ListDVRChapters.model_rebuild()
ListDVRChaptersDvrChapters.model_rebuild()

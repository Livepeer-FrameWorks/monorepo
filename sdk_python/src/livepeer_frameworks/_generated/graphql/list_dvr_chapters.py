from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import DVRChapterRef


class ListDVRChapters(BaseModel):
    dvr_chapters: Optional["ListDVRChaptersDvrChapters"] = Field(alias="dvrChapters")


class ListDVRChaptersDvrChapters(BaseModel):
    chapters: list["ListDVRChaptersDvrChaptersChapters"]
    next_page_token: Optional[str] = Field(alias="nextPageToken")


ListDVRChaptersDvrChaptersChapters = DVRChapterRef
ListDVRChapters.model_rebuild()
ListDVRChaptersDvrChapters.model_rebuild()

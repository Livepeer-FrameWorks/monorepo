from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import DVRChapterState


class GetDVRChapter(BaseModel):
    dvr_chapter: Optional["GetDVRChapterDvrChapter"] = Field(alias="dvrChapter")


class GetDVRChapterDvrChapter(BaseModel):
    chapter_id: str = Field(alias="chapterId")
    state: DVRChapterState
    playback_id: Optional[str] = Field(alias="playbackId")
    is_current: bool = Field(alias="isCurrent")
    has_gaps: bool = Field(alias="hasGaps")
    segment_count: int = Field(alias="segmentCount")
    wall_clock_start_unix_ms: float = Field(alias="wallClockStartUnixMs")
    wall_clock_end_unix_ms: float = Field(alias="wallClockEndUnixMs")
    playable_now: bool = Field(alias="playableNow")
    last_failure_reason: Optional[str] = Field(alias="lastFailureReason")


GetDVRChapter.model_rebuild()

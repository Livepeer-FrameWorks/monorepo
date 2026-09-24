from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .enums import DVRChapterState


class GetDVRChapter(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    dvr_chapter: Optional["GetDVRChapterDvrChapter"] = Field(
        alias="dvrChapter",
        description="Retrieve a single DVR chapter, including its finalized playbackId.\n\nChapters are the saved parts of a recording, produced by the\nfinalization queue as canonical .mkv VOD artifacts. The chapter mode is\nconfigured at the Stream level (Stream.dvrChapterMode) and snapshotted\nwhen the recording starts. Modes:\n  - WINDOW_SIZED (default): sequential parts as long as the recording's\n    own live rewind window, from the recording's start.\n  - FIXED_INTERVAL: UTC-only buckets of intervalSeconds, anchored at\n    unix epoch 0.\nA NONE recording keeps live rewind only and has no chapters.",
    )
    "Retrieve a single DVR chapter, including its finalized playbackId.\n\nChapters are the saved parts of a recording, produced by the\nfinalization queue as canonical .mkv VOD artifacts. The chapter mode is\nconfigured at the Stream level (Stream.dvrChapterMode) and snapshotted\nwhen the recording starts. Modes:\n  - WINDOW_SIZED (default): sequential parts as long as the recording's\n    own live rewind window, from the recording's start.\n  - FIXED_INTERVAL: UTC-only buckets of intervalSeconds, anchored at\n    unix epoch 0.\nA NONE recording keeps live rewind only and has no chapters."


class GetDVRChapterDvrChapter(BaseModel):
    """A single chapter view of a DVR recording. playbackId is the
    Commodore-minted public key for the chapter's VOD-shaped artifact;
    chapter playback uses the same artifact playback path as any VOD
    (not dvr+)."""

    chapter_id: str = Field(alias="chapterId")
    state: DVRChapterState
    playback_id: Optional[str] = Field(
        alias="playbackId",
        description="Public playback key minted by Commodore; null until finalization dispatches.",
    )
    "Public playback key minted by Commodore; null until finalization dispatches."
    is_current: bool = Field(alias="isCurrent")
    has_gaps: bool = Field(alias="hasGaps")
    segment_count: int = Field(alias="segmentCount")
    wall_clock_start_unix_ms: float = Field(
        alias="wallClockStartUnixMs",
        description="Absolute Unix epoch ms of the playable MKV span start; falls back to the scheduled chapter start before finalization.",
    )
    "Absolute Unix epoch ms of the playable MKV span start; falls back to the scheduled chapter start before finalization."
    wall_clock_end_unix_ms: float = Field(
        alias="wallClockEndUnixMs",
        description="Absolute Unix epoch ms of the playable MKV span end; falls back to the scheduled chapter end before finalization.",
    )
    "Absolute Unix epoch ms of the playable MKV span end; falls back to the scheduled chapter end before finalization."
    playable_now: bool = Field(
        alias="playableNow",
        description="True when state ∈ {FINALIZED, FROZEN, RECLAIMED}.",
    )
    "True when state ∈ {FINALIZED, FROZEN, RECLAIMED}."
    last_failure_reason: Optional[str] = Field(
        alias="lastFailureReason",
        description="Last finalization failure message (operator-facing); null on success or in-progress.",
    )
    "Last finalization failure message (operator-facing); null on success or in-progress."


GetDVRChapter.model_rebuild()

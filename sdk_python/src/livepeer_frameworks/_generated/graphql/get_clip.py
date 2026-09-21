from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    Clip,
    ClipEffectiveRetention,
    ClipPlaybackPolicy,
    ClipThumbnailAssets,
)


class GetClip(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    clip: Optional["GetClipClip"] = Field(
        description="Fetch a single clip by its global ID."
    )
    "Fetch a single clip by its global ID."


class GetClipClip(Clip):
    """A video clip extracted from a live stream's DVR buffer.
    Clips are created from recorded stream segments and stored for playback."""

    pass


GetClip.model_rebuild()

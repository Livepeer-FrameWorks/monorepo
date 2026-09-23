from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    VodRetentionDefault,
    VodRetentionDefaultPoints,  # noqa: F401
)


class GetVodRetention(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetVodRetentionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetVodRetentionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetVodRetentionAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetVodRetentionAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    vod_retention: "GetVodRetentionAnalyticsHealthVodRetention" = Field(
        alias="vodRetention",
        description='VOD retention curve for one artifact: per-bucket watched-seconds (density / "most\nreplayed") plus reached-count audience retention (sessions reaching each bucket).\n`artifactHash` is required.',
    )
    'VOD retention curve for one artifact: per-bucket watched-seconds (density / "most\nreplayed") plus reached-count audience retention (sessions reaching each bucket).\n`artifactHash` is required.'


class GetVodRetentionAnalyticsHealthVodRetention(VodRetentionDefault):
    """VOD retention curve for an artifact. retention(T) = points[T].reached /
    totalSessions (monotonic non-increasing); density(T) = points[T].secondsWatched
    (the "most replayed" curve). Buckets are fixed-width (bucketWidthS) along the asset
    timeline. These differ because a seek-to-end raises reach without adding density."""

    pass


GetVodRetention.model_rebuild()
GetVodRetentionAnalytics.model_rebuild()
GetVodRetentionAnalyticsHealth.model_rebuild()

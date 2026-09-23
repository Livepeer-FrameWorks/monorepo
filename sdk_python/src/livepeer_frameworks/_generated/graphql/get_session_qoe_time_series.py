from pydantic import Field

from .base_model import BaseModel
from .fragments import SessionQoeTimeSeriesBucketDefault


class GetSessionQoeTimeSeries(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetSessionQoeTimeSeriesAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetSessionQoeTimeSeriesAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetSessionQoeTimeSeriesAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetSessionQoeTimeSeriesAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    session_qoe_time_series: list[
        "GetSessionQoeTimeSeriesAnalyticsHealthSessionQoeTimeSeries"
    ] = Field(
        alias="sessionQoeTimeSeries",
        description="Viewer-experienced QoE bucketed over time (per-`interval` rebuffering/frame-drop/\nbitrate). Companion to sessionQoeSummary for trend charts. `sessionCount` and\n`playedHours` per bucket are the denominators for low-sample windows.",
    )
    "Viewer-experienced QoE bucketed over time (per-`interval` rebuffering/frame-drop/\nbitrate). Companion to sessionQoeSummary for trend charts. `sessionCount` and\n`playedHours` per bucket are the denominators for low-sample windows."


class GetSessionQoeTimeSeriesAnalyticsHealthSessionQoeTimeSeries(
    SessionQoeTimeSeriesBucketDefault
):
    """One toStartOfInterval window of the viewer-experienced QoE summary. Ratios are
    sum(numerator)/sum(denominator) over the per-session rollup; `sessionCount` and
    `playedHours` are the per-bucket denominators."""

    pass


GetSessionQoeTimeSeries.model_rebuild()
GetSessionQoeTimeSeriesAnalytics.model_rebuild()
GetSessionQoeTimeSeriesAnalyticsHealth.model_rebuild()

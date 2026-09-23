from pydantic import Field

from .base_model import BaseModel
from .fragments import StreamHealthSummaryDefault


class GetStreamHealthSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamHealthSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamHealthSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetStreamHealthSummaryAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetStreamHealthSummaryAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_health_summary: "GetStreamHealthSummaryAnalyticsHealthStreamHealthSummary" = Field(
        alias="streamHealthSummary",
        description="Pre-aggregated health summary for dashboard views.\nReplaces paginated streamHealthConnection when only scalar stats are needed.",
    )
    "Pre-aggregated health summary for dashboard views.\nReplaces paginated streamHealthConnection when only scalar stats are needed."


class GetStreamHealthSummaryAnalyticsHealthStreamHealthSummary(
    StreamHealthSummaryDefault
):
    """Pre-aggregated stream health summary from stream_health_5m."""

    pass


GetStreamHealthSummary.model_rebuild()
GetStreamHealthSummaryAnalytics.model_rebuild()
GetStreamHealthSummaryAnalyticsHealth.model_rebuild()

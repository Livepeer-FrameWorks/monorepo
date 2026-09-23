from pydantic import Field

from .base_model import BaseModel
from .fragments import StreamAnalyticsSummaryDefault


class GetStreamAnalyticsSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamAnalyticsSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamAnalyticsSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetStreamAnalyticsSummaryAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetStreamAnalyticsSummaryAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetStreamAnalyticsSummaryAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetStreamAnalyticsSummaryAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_analytics_summary: "GetStreamAnalyticsSummaryAnalyticsUsageStreamingStreamAnalyticsSummary" = Field(
        alias="streamAnalyticsSummary"
    )


GetStreamAnalyticsSummaryAnalyticsUsageStreamingStreamAnalyticsSummary = (
    StreamAnalyticsSummaryDefault
)
GetStreamAnalyticsSummary.model_rebuild()
GetStreamAnalyticsSummaryAnalytics.model_rebuild()
GetStreamAnalyticsSummaryAnalyticsUsage.model_rebuild()
GetStreamAnalyticsSummaryAnalyticsUsageStreaming.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamAnalyticsSummaryDefault


class GetStreamAnalyticsSummariesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamAnalyticsSummariesConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamAnalyticsSummariesConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetStreamAnalyticsSummariesConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetStreamAnalyticsSummariesConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_analytics_summaries_connection: "GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnection" = Field(
        alias="streamAnalyticsSummariesConnection",
        description="Pre-aggregated analytics summaries for multiple streams.\nReturns sorted, paginated results with tenant-wide share percentages.",
    )
    "Pre-aggregated analytics summaries for multiple streams.\nReturns sorted, paginated results with tenant-wide share percentages."


class GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnection(
    BaseModel
):
    """Connection for paginated stream analytics summaries.
    Returns pre-aggregated summaries for multiple streams with share percentages."""

    edges: list[
        "GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionEdges"
    ]
    page_info: "GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionEdges(
    BaseModel
):
    """Edge for StreamAnalyticsSummary connection."""

    cursor: str
    node: "GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionEdgesNode"


GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionEdgesNode = StreamAnalyticsSummaryDefault
GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionPageInfo = PageInfoDefault
GetStreamAnalyticsSummariesConnection.model_rebuild()
GetStreamAnalyticsSummariesConnectionAnalytics.model_rebuild()
GetStreamAnalyticsSummariesConnectionAnalyticsUsage.model_rebuild()
GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreaming.model_rebuild()
GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnection.model_rebuild()
GetStreamAnalyticsSummariesConnectionAnalyticsUsageStreamingStreamAnalyticsSummariesConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamAnalyticsDailyDefault


class GetStreamAnalyticsDailyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamAnalyticsDailyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamAnalyticsDailyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetStreamAnalyticsDailyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetStreamAnalyticsDailyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetStreamAnalyticsDailyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetStreamAnalyticsDailyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_analytics_daily_connection: "GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnection" = Field(
        alias="streamAnalyticsDailyConnection"
    )


class GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnection(
    BaseModel
):
    edges: list[
        "GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionEdges"
    ]
    page_info: "GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionEdgesNode"


GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionEdgesNode = StreamAnalyticsDailyDefault
GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionPageInfo = PageInfoDefault
GetStreamAnalyticsDailyConnection.model_rebuild()
GetStreamAnalyticsDailyConnectionAnalytics.model_rebuild()
GetStreamAnalyticsDailyConnectionAnalyticsUsage.model_rebuild()
GetStreamAnalyticsDailyConnectionAnalyticsUsageStreaming.model_rebuild()
GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnection.model_rebuild()
GetStreamAnalyticsDailyConnectionAnalyticsUsageStreamingStreamAnalyticsDailyConnectionEdges.model_rebuild()

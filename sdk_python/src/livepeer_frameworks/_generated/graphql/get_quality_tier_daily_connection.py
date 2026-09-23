from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, QualityTierDailyDefault


class GetQualityTierDailyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetQualityTierDailyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetQualityTierDailyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetQualityTierDailyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetQualityTierDailyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetQualityTierDailyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetQualityTierDailyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    quality_tier_daily_connection: "GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnection" = Field(
        alias="qualityTierDailyConnection"
    )


class GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnection(
    BaseModel
):
    edges: list[
        "GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionEdges"
    ]
    page_info: "GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionEdgesNode"


GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionEdgesNode = QualityTierDailyDefault
GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionPageInfo = PageInfoDefault
GetQualityTierDailyConnection.model_rebuild()
GetQualityTierDailyConnectionAnalytics.model_rebuild()
GetQualityTierDailyConnectionAnalyticsUsage.model_rebuild()
GetQualityTierDailyConnectionAnalyticsUsageStreaming.model_rebuild()
GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnection.model_rebuild()
GetQualityTierDailyConnectionAnalyticsUsageStreamingQualityTierDailyConnectionEdges.model_rebuild()

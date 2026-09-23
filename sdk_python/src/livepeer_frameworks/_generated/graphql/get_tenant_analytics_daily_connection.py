from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, TenantAnalyticsDailyDefault


class GetTenantAnalyticsDailyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetTenantAnalyticsDailyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetTenantAnalyticsDailyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetTenantAnalyticsDailyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetTenantAnalyticsDailyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetTenantAnalyticsDailyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetTenantAnalyticsDailyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    tenant_analytics_daily_connection: "GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnection" = Field(
        alias="tenantAnalyticsDailyConnection"
    )


class GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnection(
    BaseModel
):
    edges: list[
        "GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionEdges"
    ]
    page_info: "GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionEdgesNode"


GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionEdgesNode = TenantAnalyticsDailyDefault
GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionPageInfo = PageInfoDefault
GetTenantAnalyticsDailyConnection.model_rebuild()
GetTenantAnalyticsDailyConnectionAnalytics.model_rebuild()
GetTenantAnalyticsDailyConnectionAnalyticsUsage.model_rebuild()
GetTenantAnalyticsDailyConnectionAnalyticsUsageStreaming.model_rebuild()
GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnection.model_rebuild()
GetTenantAnalyticsDailyConnectionAnalyticsUsageStreamingTenantAnalyticsDailyConnectionEdges.model_rebuild()

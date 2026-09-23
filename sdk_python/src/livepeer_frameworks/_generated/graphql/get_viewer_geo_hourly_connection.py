from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ViewerGeoHourlyDefault


class GetViewerGeoHourlyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetViewerGeoHourlyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetViewerGeoHourlyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetViewerGeoHourlyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetViewerGeoHourlyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetViewerGeoHourlyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetViewerGeoHourlyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    viewer_geo_hourly_connection: "GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnection" = Field(
        alias="viewerGeoHourlyConnection"
    )


class GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnection(
    BaseModel
):
    edges: list[
        "GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionEdges"
    ]
    page_info: "GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionEdgesNode"


GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionEdgesNode = ViewerGeoHourlyDefault
GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionPageInfo = (
    PageInfoDefault
)
GetViewerGeoHourlyConnection.model_rebuild()
GetViewerGeoHourlyConnectionAnalytics.model_rebuild()
GetViewerGeoHourlyConnectionAnalyticsUsage.model_rebuild()
GetViewerGeoHourlyConnectionAnalyticsUsageStreaming.model_rebuild()
GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnection.model_rebuild()
GetViewerGeoHourlyConnectionAnalyticsUsageStreamingViewerGeoHourlyConnectionEdges.model_rebuild()

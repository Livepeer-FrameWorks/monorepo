from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ViewerCountBucketDefault


class GetViewerTimeSeriesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetViewerTimeSeriesConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetViewerTimeSeriesConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetViewerTimeSeriesConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetViewerTimeSeriesConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetViewerTimeSeriesConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetViewerTimeSeriesConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    viewer_time_series_connection: "GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnection" = Field(
        alias="viewerTimeSeriesConnection"
    )


class GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnection(
    BaseModel
):
    edges: list[
        "GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionEdges"
    ]
    page_info: "GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionEdgesNode"


GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionEdgesNode = ViewerCountBucketDefault
GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionPageInfo = PageInfoDefault
GetViewerTimeSeriesConnection.model_rebuild()
GetViewerTimeSeriesConnectionAnalytics.model_rebuild()
GetViewerTimeSeriesConnectionAnalyticsUsage.model_rebuild()
GetViewerTimeSeriesConnectionAnalyticsUsageStreaming.model_rebuild()
GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnection.model_rebuild()
GetViewerTimeSeriesConnectionAnalyticsUsageStreamingViewerTimeSeriesConnectionEdges.model_rebuild()

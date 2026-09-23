from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ViewerHoursHourlyDefault


class GetViewerHoursHourlyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetViewerHoursHourlyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetViewerHoursHourlyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetViewerHoursHourlyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetViewerHoursHourlyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetViewerHoursHourlyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetViewerHoursHourlyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    viewer_hours_hourly_connection: "GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnection" = Field(
        alias="viewerHoursHourlyConnection"
    )


class GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnection(
    BaseModel
):
    edges: list[
        "GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionEdges"
    ]
    page_info: "GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionEdgesNode"


GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionEdgesNode = ViewerHoursHourlyDefault
GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionPageInfo = PageInfoDefault
GetViewerHoursHourlyConnection.model_rebuild()
GetViewerHoursHourlyConnectionAnalytics.model_rebuild()
GetViewerHoursHourlyConnectionAnalyticsUsage.model_rebuild()
GetViewerHoursHourlyConnectionAnalyticsUsageStreaming.model_rebuild()
GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnection.model_rebuild()
GetViewerHoursHourlyConnectionAnalyticsUsageStreamingViewerHoursHourlyConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ViewerGeographicDefault


class GetViewerGeographicsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetViewerGeographicsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetViewerGeographicsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetViewerGeographicsConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetViewerGeographicsConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetViewerGeographicsConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetViewerGeographicsConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    viewer_geographics_connection: "GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnection" = Field(
        alias="viewerGeographicsConnection"
    )


class GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnection(
    BaseModel
):
    edges: list[
        "GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionEdges"
    ]
    page_info: "GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionEdgesNode"


GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionEdgesNode = ViewerGeographicDefault
GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionPageInfo = PageInfoDefault
GetViewerGeographicsConnection.model_rebuild()
GetViewerGeographicsConnectionAnalytics.model_rebuild()
GetViewerGeographicsConnectionAnalyticsUsage.model_rebuild()
GetViewerGeographicsConnectionAnalyticsUsageStreaming.model_rebuild()
GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnection.model_rebuild()
GetViewerGeographicsConnectionAnalyticsUsageStreamingViewerGeographicsConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamConnectionHourlyDefault


class GetStreamConnectionHourlyConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamConnectionHourlyConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamConnectionHourlyConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetStreamConnectionHourlyConnectionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetStreamConnectionHourlyConnectionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetStreamConnectionHourlyConnectionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetStreamConnectionHourlyConnectionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_connection_hourly_connection: "GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnection" = Field(
        alias="streamConnectionHourlyConnection"
    )


class GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnection(
    BaseModel
):
    edges: list[
        "GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionEdges"
    ]
    page_info: "GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionEdgesNode"


GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionEdgesNode = StreamConnectionHourlyDefault
GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionPageInfo = PageInfoDefault
GetStreamConnectionHourlyConnection.model_rebuild()
GetStreamConnectionHourlyConnectionAnalytics.model_rebuild()
GetStreamConnectionHourlyConnectionAnalyticsUsage.model_rebuild()
GetStreamConnectionHourlyConnectionAnalyticsUsageStreaming.model_rebuild()
GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnection.model_rebuild()
GetStreamConnectionHourlyConnectionAnalyticsUsageStreamingStreamConnectionHourlyConnectionEdges.model_rebuild()

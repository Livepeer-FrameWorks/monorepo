from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamHealthMetricDefault


class GetStreamHealthConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamHealthConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamHealthConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetStreamHealthConnectionAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetStreamHealthConnectionAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_health_connection: "GetStreamHealthConnectionAnalyticsHealthStreamHealthConnection" = Field(
        alias="streamHealthConnection"
    )


class GetStreamHealthConnectionAnalyticsHealthStreamHealthConnection(BaseModel):
    edges: list["GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionEdges"]
    page_info: "GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionEdges(BaseModel):
    cursor: str
    node: "GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionEdgesNode"


GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionEdgesNode = (
    StreamHealthMetricDefault
)
GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionPageInfo = PageInfoDefault
GetStreamHealthConnection.model_rebuild()
GetStreamHealthConnectionAnalytics.model_rebuild()
GetStreamHealthConnectionAnalyticsHealth.model_rebuild()
GetStreamHealthConnectionAnalyticsHealthStreamHealthConnection.model_rebuild()
GetStreamHealthConnectionAnalyticsHealthStreamHealthConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamHealth5mDefault


class GetStreamHealth5mConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamHealth5mConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamHealth5mConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetStreamHealth5mConnectionAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetStreamHealth5mConnectionAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_health_5_m_connection: "GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnection" = Field(
        alias="streamHealth5mConnection"
    )


class GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnection(BaseModel):
    edges: list[
        "GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionEdges"
    ]
    page_info: "GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionEdgesNode"


GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionEdgesNode = (
    StreamHealth5mDefault
)
GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionPageInfo = (
    PageInfoDefault
)
GetStreamHealth5mConnection.model_rebuild()
GetStreamHealth5mConnectionAnalytics.model_rebuild()
GetStreamHealth5mConnectionAnalyticsHealth.model_rebuild()
GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnection.model_rebuild()
GetStreamHealth5mConnectionAnalyticsHealthStreamHealth5MConnectionEdges.model_rebuild()

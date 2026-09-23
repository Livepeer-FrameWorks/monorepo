from pydantic import Field

from .base_model import BaseModel
from .fragments import NodePerformance5mDefault, PageInfoDefault


class GetNodePerformance5mConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetNodePerformance5mConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetNodePerformance5mConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetNodePerformance5mConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetNodePerformance5mConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    node_performance_5_m_connection: "GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnection" = Field(
        alias="nodePerformance5mConnection"
    )


class GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnection(
    BaseModel
):
    edges: list[
        "GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionEdges"
    ]
    page_info: "GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionEdgesNode"


GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionEdgesNode = (
    NodePerformance5mDefault
)
GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionPageInfo = (
    PageInfoDefault
)
GetNodePerformance5mConnection.model_rebuild()
GetNodePerformance5mConnectionAnalytics.model_rebuild()
GetNodePerformance5mConnectionAnalyticsInfra.model_rebuild()
GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnection.model_rebuild()
GetNodePerformance5mConnectionAnalyticsInfraNodePerformance5MConnectionEdges.model_rebuild()

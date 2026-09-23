from pydantic import Field

from .base_model import BaseModel
from .fragments import NodeMetricHourlyDefault, PageInfoDefault


class GetNodeMetrics1hConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetNodeMetrics1hConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetNodeMetrics1hConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetNodeMetrics1hConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetNodeMetrics1hConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    node_metrics_1_h_connection: "GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnection" = Field(
        alias="nodeMetrics1hConnection"
    )


class GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnection(BaseModel):
    edges: list["GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionEdges"]
    page_info: "GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionEdges(BaseModel):
    cursor: str
    node: "GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionEdgesNode"


GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionEdgesNode = (
    NodeMetricHourlyDefault
)
GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionPageInfo = (
    PageInfoDefault
)
GetNodeMetrics1hConnection.model_rebuild()
GetNodeMetrics1hConnectionAnalytics.model_rebuild()
GetNodeMetrics1hConnectionAnalyticsInfra.model_rebuild()
GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnection.model_rebuild()
GetNodeMetrics1hConnectionAnalyticsInfraNodeMetrics1HConnectionEdges.model_rebuild()

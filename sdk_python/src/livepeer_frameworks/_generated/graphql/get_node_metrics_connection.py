from pydantic import Field

from .base_model import BaseModel
from .fragments import NodeMetricDefault, PageInfoDefault


class GetNodeMetricsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetNodeMetricsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetNodeMetricsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetNodeMetricsConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetNodeMetricsConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    node_metrics_connection: "GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnection" = Field(
        alias="nodeMetricsConnection"
    )


class GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnection(BaseModel):
    edges: list["GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionEdges"]
    page_info: "GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionEdges(BaseModel):
    cursor: str
    node: "GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionEdgesNode"


GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionEdgesNode = NodeMetricDefault
GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionPageInfo = PageInfoDefault
GetNodeMetricsConnection.model_rebuild()
GetNodeMetricsConnectionAnalytics.model_rebuild()
GetNodeMetricsConnectionAnalyticsInfra.model_rebuild()
GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnection.model_rebuild()
GetNodeMetricsConnectionAnalyticsInfraNodeMetricsConnectionEdges.model_rebuild()

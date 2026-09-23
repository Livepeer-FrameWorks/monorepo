from pydantic import Field

from .base_model import BaseModel
from .fragments import NodeMetricsAggregatedDefault


class GetNodeMetricsAggregated(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetNodeMetricsAggregatedAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetNodeMetricsAggregatedAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetNodeMetricsAggregatedAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetNodeMetricsAggregatedAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    node_metrics_aggregated: list[
        "GetNodeMetricsAggregatedAnalyticsInfraNodeMetricsAggregated"
    ] = Field(alias="nodeMetricsAggregated")


GetNodeMetricsAggregatedAnalyticsInfraNodeMetricsAggregated = (
    NodeMetricsAggregatedDefault
)
GetNodeMetricsAggregated.model_rebuild()
GetNodeMetricsAggregatedAnalytics.model_rebuild()
GetNodeMetricsAggregatedAnalyticsInfra.model_rebuild()

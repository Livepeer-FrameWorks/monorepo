from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterPairTrafficDefault


class GetClusterTrafficMatrix(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClusterTrafficMatrixAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClusterTrafficMatrixAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetClusterTrafficMatrixAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetClusterTrafficMatrixAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    cluster_traffic_matrix: list[
        "GetClusterTrafficMatrixAnalyticsInfraClusterTrafficMatrix"
    ] = Field(
        alias="clusterTrafficMatrix",
        description="Cross-cluster routing traffic matrix from hourly rollups.",
    )
    "Cross-cluster routing traffic matrix from hourly rollups."


GetClusterTrafficMatrixAnalyticsInfraClusterTrafficMatrix = ClusterPairTrafficDefault
GetClusterTrafficMatrix.model_rebuild()
GetClusterTrafficMatrixAnalytics.model_rebuild()
GetClusterTrafficMatrixAnalyticsInfra.model_rebuild()

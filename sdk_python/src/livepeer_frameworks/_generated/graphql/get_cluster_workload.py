from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterWorkloadDefault


class GetClusterWorkload(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClusterWorkloadAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClusterWorkloadAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetClusterWorkloadAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetClusterWorkloadAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    cluster_workload: list["GetClusterWorkloadAnalyticsInfraClusterWorkload"] = Field(
        alias="clusterWorkload",
        description="Aggregate/redacted media work on clusters the caller owns. Contains placement\nand volume only; never tenant, content, stream, session, URL, or client IDs.",
    )
    "Aggregate/redacted media work on clusters the caller owns. Contains placement\nand volume only; never tenant, content, stream, session, URL, or client IDs."


GetClusterWorkloadAnalyticsInfraClusterWorkload = ClusterWorkloadDefault
GetClusterWorkload.model_rebuild()
GetClusterWorkloadAnalytics.model_rebuild()
GetClusterWorkloadAnalyticsInfra.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterBootOpsDefault


class GetClusterBootOps(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClusterBootOpsAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClusterBootOpsAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetClusterBootOpsAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetClusterBootOpsAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    cluster_boot_ops: list["GetClusterBootOpsAnalyticsInfraClusterBootOps"] = Field(
        alias="clusterBootOps",
        description="Player startup (boot) operations aggregate for clusters the caller owns.\nAggregate/redacted; only token-attributed boot rows are included.",
    )
    "Player startup (boot) operations aggregate for clusters the caller owns.\nAggregate/redacted; only token-attributed boot rows are included."


class GetClusterBootOpsAnalyticsInfraClusterBootOps(ClusterBootOpsDefault):
    """Cluster-ops boot aggregate for operators of the serving cluster. Aggregate and
    redacted: never exposes content/stream/session/URL/tenant identifiers. Populated
    only from token-attributed rows."""

    pass


GetClusterBootOps.model_rebuild()
GetClusterBootOpsAnalytics.model_rebuild()
GetClusterBootOpsAnalyticsInfra.model_rebuild()

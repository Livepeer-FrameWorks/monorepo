from pydantic import Field

from .base_model import BaseModel
from .fragments import ClusterQoeOpsDefault


class GetClusterQoeOps(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClusterQoeOpsAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClusterQoeOpsAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetClusterQoeOpsAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetClusterQoeOpsAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    cluster_qoe_ops: list["GetClusterQoeOpsAnalyticsInfraClusterQoeOps"] = Field(
        alias="clusterQoeOps",
        description="Cluster-ops viewer-QoE aggregate per serving node/protocol, for operators of\nclusters they own. Aggregate/redacted; only token-attributed rows are included.",
    )
    "Cluster-ops viewer-QoE aggregate per serving node/protocol, for operators of\nclusters they own. Aggregate/redacted; only token-attributed rows are included."


class GetClusterQoeOpsAnalyticsInfraClusterQoeOps(ClusterQoeOpsDefault):
    """Cluster-ops viewer-QoE aggregate per serving node/protocol (token-attributed,
    redacted — no content/stream/session identifiers)."""

    pass


GetClusterQoeOps.model_rebuild()
GetClusterQoeOpsAnalytics.model_rebuild()
GetClusterQoeOpsAnalyticsInfra.model_rebuild()

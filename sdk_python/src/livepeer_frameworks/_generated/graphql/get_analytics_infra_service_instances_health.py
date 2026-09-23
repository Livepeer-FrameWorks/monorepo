from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import ServiceInstanceHealthDefault


class GetAnalyticsInfraServiceInstancesHealth(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetAnalyticsInfraServiceInstancesHealthAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetAnalyticsInfraServiceInstancesHealthAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetAnalyticsInfraServiceInstancesHealthAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetAnalyticsInfraServiceInstancesHealthAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    service_instances_health: Optional[
        list[
            "GetAnalyticsInfraServiceInstancesHealthAnalyticsInfraServiceInstancesHealth"
        ]
    ] = Field(alias="serviceInstancesHealth")


GetAnalyticsInfraServiceInstancesHealthAnalyticsInfraServiceInstancesHealth = (
    ServiceInstanceHealthDefault
)
GetAnalyticsInfraServiceInstancesHealth.model_rebuild()
GetAnalyticsInfraServiceInstancesHealthAnalytics.model_rebuild()
GetAnalyticsInfraServiceInstancesHealthAnalyticsInfra.model_rebuild()

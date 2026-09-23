from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ServiceInstanceDefault


class GetServiceInstancesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetServiceInstancesConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetServiceInstancesConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetServiceInstancesConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetServiceInstancesConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    service_instances_connection: "GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnection" = Field(
        alias="serviceInstancesConnection"
    )


class GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnection(BaseModel):
    edges: list[
        "GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionEdges"
    ]
    page_info: "GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionEdgesNode"
    )


GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionEdgesNode = (
    ServiceInstanceDefault
)
GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionPageInfo = (
    PageInfoDefault
)
GetServiceInstancesConnection.model_rebuild()
GetServiceInstancesConnectionAnalytics.model_rebuild()
GetServiceInstancesConnectionAnalyticsInfra.model_rebuild()
GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnection.model_rebuild()
GetServiceInstancesConnectionAnalyticsInfraServiceInstancesConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, RoutingEventDefault


class GetRoutingEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetRoutingEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetRoutingEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetRoutingEventsConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetRoutingEventsConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    routing_events_connection: "GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnection" = Field(
        alias="routingEventsConnection"
    )


class GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnection(BaseModel):
    edges: list["GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionEdges"]
    page_info: "GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionEdges(BaseModel):
    cursor: str
    node: "GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionEdgesNode"


GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionEdgesNode = (
    RoutingEventDefault
)
GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionPageInfo = (
    PageInfoDefault
)
GetRoutingEventsConnection.model_rebuild()
GetRoutingEventsConnectionAnalytics.model_rebuild()
GetRoutingEventsConnectionAnalyticsInfra.model_rebuild()
GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnection.model_rebuild()
GetRoutingEventsConnectionAnalyticsInfraRoutingEventsConnectionEdges.model_rebuild()

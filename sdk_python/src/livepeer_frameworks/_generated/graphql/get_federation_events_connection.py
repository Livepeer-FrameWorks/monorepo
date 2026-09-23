from pydantic import Field

from .base_model import BaseModel
from .fragments import FederationEventDefault, PageInfoDefault


class GetFederationEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetFederationEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetFederationEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetFederationEventsConnectionAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetFederationEventsConnectionAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    federation_events_connection: "GetFederationEventsConnectionAnalyticsInfraFederationEventsConnection" = Field(
        alias="federationEventsConnection",
        description="Federation events: origin pulls, peer connections, leader elections, etc.",
    )
    "Federation events: origin pulls, peer connections, leader elections, etc."


class GetFederationEventsConnectionAnalyticsInfraFederationEventsConnection(BaseModel):
    edges: list[
        "GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionEdges"
    ]
    page_info: "GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionEdgesNode"
    )


GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionEdgesNode = (
    FederationEventDefault
)
GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionPageInfo = (
    PageInfoDefault
)
GetFederationEventsConnection.model_rebuild()
GetFederationEventsConnectionAnalytics.model_rebuild()
GetFederationEventsConnectionAnalyticsInfra.model_rebuild()
GetFederationEventsConnectionAnalyticsInfraFederationEventsConnection.model_rebuild()
GetFederationEventsConnectionAnalyticsInfraFederationEventsConnectionEdges.model_rebuild()

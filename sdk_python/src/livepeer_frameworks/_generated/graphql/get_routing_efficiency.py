from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    RoutingEfficiencyDefault,
    RoutingEfficiencyDefaultTopCountries,  # noqa: F401
)


class GetRoutingEfficiency(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetRoutingEfficiencyAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetRoutingEfficiencyAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetRoutingEfficiencyAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetRoutingEfficiencyAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    routing_efficiency: "GetRoutingEfficiencyAnalyticsInfraRoutingEfficiency" = Field(
        alias="routingEfficiency",
        description="Pre-aggregated routing efficiency summary for dashboard views.\nReplaces client-side aggregation of raw routingEventsConnection data.",
    )
    "Pre-aggregated routing efficiency summary for dashboard views.\nReplaces client-side aggregation of raw routingEventsConnection data."


class GetRoutingEfficiencyAnalyticsInfraRoutingEfficiency(RoutingEfficiencyDefault):
    """Pre-aggregated routing efficiency summary (replaces client-side aggregation of raw routing events)."""

    pass


GetRoutingEfficiency.model_rebuild()
GetRoutingEfficiencyAnalytics.model_rebuild()
GetRoutingEfficiencyAnalyticsInfra.model_rebuild()

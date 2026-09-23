from pydantic import Field

from .base_model import BaseModel
from .fragments import FederationSummaryDefault


class GetFederationSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetFederationSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetFederationSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    infra: "GetFederationSummaryAnalyticsInfra" = Field(
        description="Infrastructure analytics: routing, node metrics, services."
    )
    "Infrastructure analytics: routing, node metrics, services."


class GetFederationSummaryAnalyticsInfra(BaseModel):
    """Infrastructure analytics.
    `streamId` arguments accept Stream.id (Relay global ID) where present."""

    federation_summary: "GetFederationSummaryAnalyticsInfraFederationSummary" = Field(
        alias="federationSummary",
        description="Aggregated federation summary: event counts by type, latency, failure rate.",
    )
    "Aggregated federation summary: event counts by type, latency, failure rate."


GetFederationSummaryAnalyticsInfraFederationSummary = FederationSummaryDefault
GetFederationSummary.model_rebuild()
GetFederationSummaryAnalytics.model_rebuild()
GetFederationSummaryAnalyticsInfra.model_rebuild()

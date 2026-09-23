from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import GeographicDistributionDefault


class GetGeographicDistribution(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetGeographicDistributionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetGeographicDistributionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    usage: "GetGeographicDistributionAnalyticsUsage" = Field(
        description="Usage analytics: streaming hours, storage, and processing."
    )
    "Usage analytics: streaming hours, storage, and processing."


class GetGeographicDistributionAnalyticsUsage(BaseModel):
    """Usage analytics grouped by type."""

    streaming: "GetGeographicDistributionAnalyticsUsageStreaming" = Field(
        description="Streaming usage: viewer hours, geographic distribution, quality tiers."
    )
    "Streaming usage: viewer hours, geographic distribution, quality tiers."


class GetGeographicDistributionAnalyticsUsageStreaming(BaseModel):
    """Streaming usage analytics.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    geographic_distribution: Optional[
        "GetGeographicDistributionAnalyticsUsageStreamingGeographicDistribution"
    ] = Field(alias="geographicDistribution")


GetGeographicDistributionAnalyticsUsageStreamingGeographicDistribution = (
    GeographicDistributionDefault
)
GetGeographicDistribution.model_rebuild()
GetGeographicDistributionAnalytics.model_rebuild()
GetGeographicDistributionAnalyticsUsage.model_rebuild()
GetGeographicDistributionAnalyticsUsageStreaming.model_rebuild()

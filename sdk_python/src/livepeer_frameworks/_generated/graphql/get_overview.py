from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PlatformOverviewDefault


class GetOverview(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetOverviewAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetOverviewAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    overview: Optional["GetOverviewAnalyticsOverview"] = Field(
        description="Platform-wide overview metrics for the given time range."
    )
    "Platform-wide overview metrics for the given time range."


GetOverviewAnalyticsOverview = PlatformOverviewDefault
GetOverview.model_rebuild()
GetOverviewAnalytics.model_rebuild()

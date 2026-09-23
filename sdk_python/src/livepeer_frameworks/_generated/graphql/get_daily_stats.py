from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import TenantDailyStatDefault


class GetDailyStats(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetDailyStatsAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetDailyStatsAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    overview: Optional["GetDailyStatsAnalyticsOverview"] = Field(
        description="Platform-wide overview metrics for the given time range."
    )
    "Platform-wide overview metrics for the given time range."


class GetDailyStatsAnalyticsOverview(BaseModel):
    daily_stats: list["GetDailyStatsAnalyticsOverviewDailyStats"] = Field(
        alias="dailyStats"
    )


GetDailyStatsAnalyticsOverviewDailyStats = TenantDailyStatDefault
GetDailyStats.model_rebuild()
GetDailyStatsAnalytics.model_rebuild()
GetDailyStatsAnalyticsOverview.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PlayerBootTimeSeriesBucketDefault


class GetPlayerBootTimeSeries(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetPlayerBootTimeSeriesAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetPlayerBootTimeSeriesAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetPlayerBootTimeSeriesAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetPlayerBootTimeSeriesAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    player_boot_time_series: list[
        "GetPlayerBootTimeSeriesAnalyticsHealthPlayerBootTimeSeries"
    ] = Field(
        alias="playerBootTimeSeries",
        description='Player startup summary bucketed over time (read-time TTF percentiles per\n`interval` window: "5m"/"15m"/"1h"/"1d"). Companion to playerBootSummary for\ntrend charts. `bootCount` per bucket is the denominator for low-sample windows.',
    )
    'Player startup summary bucketed over time (read-time TTF percentiles per\n`interval` window: "5m"/"15m"/"1h"/"1d"). Companion to playerBootSummary for\ntrend charts. `bootCount` per bucket is the denominator for low-sample windows.'


class GetPlayerBootTimeSeriesAnalyticsHealthPlayerBootTimeSeries(
    PlayerBootTimeSeriesBucketDefault
):
    """One toStartOfInterval window of the boot-startup summary. TTF percentiles are
    computed at read time per bucket; `bootCount` is the per-bucket denominator."""

    pass


GetPlayerBootTimeSeries.model_rebuild()
GetPlayerBootTimeSeriesAnalytics.model_rebuild()
GetPlayerBootTimeSeriesAnalyticsHealth.model_rebuild()

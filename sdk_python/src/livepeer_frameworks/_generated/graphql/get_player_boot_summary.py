from pydantic import Field

from .base_model import BaseModel
from .fragments import PlayerBootSummaryDefault


class GetPlayerBootSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetPlayerBootSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetPlayerBootSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetPlayerBootSummaryAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetPlayerBootSummaryAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    player_boot_summary: "GetPlayerBootSummaryAnalyticsHealthPlayerBootSummary" = Field(
        alias="playerBootSummary",
        description="Player startup (boot) summary for the tenant that owns the content. Diagnostic.",
    )
    "Player startup (boot) summary for the tenant that owns the content. Diagnostic."


class GetPlayerBootSummaryAnalyticsHealthPlayerBootSummary(PlayerBootSummaryDefault):
    """Player startup (boot) summary from player_boot_samples. Diagnostic only — never
    a viewer-count or billing source. TTF percentiles are computed at read time over
    boots that reached first frame; counts cover all rows."""

    pass


GetPlayerBootSummary.model_rebuild()
GetPlayerBootSummaryAnalytics.model_rebuild()
GetPlayerBootSummaryAnalyticsHealth.model_rebuild()

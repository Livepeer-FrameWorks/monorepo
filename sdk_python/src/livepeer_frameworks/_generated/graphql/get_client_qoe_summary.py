from pydantic import Field

from .base_model import BaseModel
from .fragments import ClientQoeSummaryDefault


class GetClientQoeSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetClientQoeSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetClientQoeSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetClientQoeSummaryAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetClientQoeSummaryAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    client_qoe_summary: "GetClientQoeSummaryAnalyticsHealthClientQoeSummary" = Field(
        alias="clientQoeSummary",
        description="Pre-aggregated client QoE summary for dashboard views.\nReplaces paginated clientQoeConnection when only scalar stats are needed.",
    )
    "Pre-aggregated client QoE summary for dashboard views.\nReplaces paginated clientQoeConnection when only scalar stats are needed."


class GetClientQoeSummaryAnalyticsHealthClientQoeSummary(ClientQoeSummaryDefault):
    """Pre-aggregated client QoE summary from client_qoe_5m."""

    pass


GetClientQoeSummary.model_rebuild()
GetClientQoeSummaryAnalytics.model_rebuild()
GetClientQoeSummaryAnalyticsHealth.model_rebuild()

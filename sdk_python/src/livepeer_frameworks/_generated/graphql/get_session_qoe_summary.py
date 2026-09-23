from pydantic import Field

from .base_model import BaseModel
from .fragments import SessionQoeSummaryDefault


class GetSessionQoeSummary(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetSessionQoeSummaryAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetSessionQoeSummaryAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetSessionQoeSummaryAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetSessionQoeSummaryAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    session_qoe_summary: "GetSessionQoeSummaryAnalyticsHealthSessionQoeSummary" = Field(
        alias="sessionQoeSummary",
        description="Viewer-experienced QoE summary (rebuffering, frame drops, bitrate, EBVS) for the\ntenant that owns the content. Read-time ratios over the player-reported session\nbeacons. Diagnostic only — never billing/viewer-count truth.",
    )
    "Viewer-experienced QoE summary (rebuffering, frame drops, bitrate, EBVS) for the\ntenant that owns the content. Read-time ratios over the player-reported session\nbeacons. Diagnostic only — never billing/viewer-count truth."


class GetSessionQoeSummaryAnalyticsHealthSessionQoeSummary(SessionQoeSummaryDefault):
    """Viewer-experienced QoE summary. Ratios are sum(numerator)/sum(denominator) over
    the player-reported session beacons; rebufferingRatio is the headline QoE number."""

    pass


GetSessionQoeSummary.model_rebuild()
GetSessionQoeSummaryAnalytics.model_rebuild()
GetSessionQoeSummaryAnalyticsHealth.model_rebuild()

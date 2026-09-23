from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, RebufferingEventDefault


class GetRebufferingEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetRebufferingEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetRebufferingEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    health: "GetRebufferingEventsConnectionAnalyticsHealth" = Field(
        description="Health analytics: stream quality, rebuffering, client QoE."
    )
    "Health analytics: stream quality, rebuffering, client QoE."


class GetRebufferingEventsConnectionAnalyticsHealth(BaseModel):
    """Health analytics for streams.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    rebuffering_events_connection: "GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnection" = Field(
        alias="rebufferingEventsConnection"
    )


class GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnection(
    BaseModel
):
    edges: list[
        "GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionEdges"
    ]
    page_info: "GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionEdgesNode"


GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionEdgesNode = (
    RebufferingEventDefault
)
GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionPageInfo = (
    PageInfoDefault
)
GetRebufferingEventsConnection.model_rebuild()
GetRebufferingEventsConnectionAnalytics.model_rebuild()
GetRebufferingEventsConnectionAnalyticsHealth.model_rebuild()
GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnection.model_rebuild()
GetRebufferingEventsConnectionAnalyticsHealthRebufferingEventsConnectionEdges.model_rebuild()

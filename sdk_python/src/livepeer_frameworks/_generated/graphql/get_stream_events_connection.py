from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamEventDefault


class GetStreamEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStreamEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStreamEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetStreamEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetStreamEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    stream_events_connection: "GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnection" = Field(
        alias="streamEventsConnection"
    )


class GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnection(BaseModel):
    edges: list[
        "GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionEdges"
    ]
    page_info: "GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionEdges(BaseModel):
    cursor: str
    node: "GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionEdgesNode"


GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionEdgesNode = (
    StreamEventDefault
)
GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionPageInfo = (
    PageInfoDefault
)
GetStreamEventsConnection.model_rebuild()
GetStreamEventsConnectionAnalytics.model_rebuild()
GetStreamEventsConnectionAnalyticsLifecycle.model_rebuild()
GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnection.model_rebuild()
GetStreamEventsConnectionAnalyticsLifecycleStreamEventsConnectionEdges.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import BufferEventDefault, PageInfoDefault


class GetBufferEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetBufferEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetBufferEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetBufferEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetBufferEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    buffer_events_connection: "GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnection" = Field(
        alias="bufferEventsConnection"
    )


class GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnection(BaseModel):
    edges: list[
        "GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionEdges"
    ]
    page_info: "GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionEdges(BaseModel):
    cursor: str
    node: "GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionEdgesNode"


GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionEdgesNode = (
    BufferEventDefault
)
GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionPageInfo = (
    PageInfoDefault
)
GetBufferEventsConnection.model_rebuild()
GetBufferEventsConnectionAnalytics.model_rebuild()
GetBufferEventsConnectionAnalyticsLifecycle.model_rebuild()
GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnection.model_rebuild()
GetBufferEventsConnectionAnalyticsLifecycleBufferEventsConnectionEdges.model_rebuild()

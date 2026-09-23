from pydantic import Field

from .base_model import BaseModel
from .fragments import ConnectionEventDefault, PageInfoDefault


class GetConnectionEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetConnectionEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetConnectionEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetConnectionEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetConnectionEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    connection_events_connection: "GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnection" = Field(
        alias="connectionEventsConnection"
    )


class GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnection(
    BaseModel
):
    edges: list[
        "GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionEdges"
    ]
    page_info: "GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionEdgesNode"


GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionEdgesNode = (
    ConnectionEventDefault
)
GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionPageInfo = (
    PageInfoDefault
)
GetConnectionEventsConnection.model_rebuild()
GetConnectionEventsConnectionAnalytics.model_rebuild()
GetConnectionEventsConnectionAnalyticsLifecycle.model_rebuild()
GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnection.model_rebuild()
GetConnectionEventsConnectionAnalyticsLifecycleConnectionEventsConnectionEdges.model_rebuild()

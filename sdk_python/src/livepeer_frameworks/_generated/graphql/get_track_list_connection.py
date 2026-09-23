from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, TrackListEventDefault


class GetTrackListConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetTrackListConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetTrackListConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetTrackListConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetTrackListConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    track_list_connection: "GetTrackListConnectionAnalyticsLifecycleTrackListConnection" = Field(
        alias="trackListConnection"
    )


class GetTrackListConnectionAnalyticsLifecycleTrackListConnection(BaseModel):
    edges: list["GetTrackListConnectionAnalyticsLifecycleTrackListConnectionEdges"]
    page_info: "GetTrackListConnectionAnalyticsLifecycleTrackListConnectionPageInfo" = (
        Field(alias="pageInfo")
    )
    total_count: int = Field(alias="totalCount")


class GetTrackListConnectionAnalyticsLifecycleTrackListConnectionEdges(BaseModel):
    cursor: str
    node: "GetTrackListConnectionAnalyticsLifecycleTrackListConnectionEdgesNode"


GetTrackListConnectionAnalyticsLifecycleTrackListConnectionEdgesNode = (
    TrackListEventDefault
)
GetTrackListConnectionAnalyticsLifecycleTrackListConnectionPageInfo = PageInfoDefault
GetTrackListConnection.model_rebuild()
GetTrackListConnectionAnalytics.model_rebuild()
GetTrackListConnectionAnalyticsLifecycle.model_rebuild()
GetTrackListConnectionAnalyticsLifecycleTrackListConnection.model_rebuild()
GetTrackListConnectionAnalyticsLifecycleTrackListConnectionEdges.model_rebuild()

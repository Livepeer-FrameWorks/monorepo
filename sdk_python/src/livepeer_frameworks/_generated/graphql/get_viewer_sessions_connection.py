from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, ViewerSessionDefault


class GetViewerSessionsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetViewerSessionsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetViewerSessionsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetViewerSessionsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetViewerSessionsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    viewer_sessions_connection: "GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnection" = Field(
        alias="viewerSessionsConnection"
    )


class GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnection(BaseModel):
    edges: list[
        "GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionEdges"
    ]
    page_info: "GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionEdgesNode"
    )


GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionEdgesNode = (
    ViewerSessionDefault
)
GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionPageInfo = (
    PageInfoDefault
)
GetViewerSessionsConnection.model_rebuild()
GetViewerSessionsConnectionAnalytics.model_rebuild()
GetViewerSessionsConnectionAnalyticsLifecycle.model_rebuild()
GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnection.model_rebuild()
GetViewerSessionsConnectionAnalyticsLifecycleViewerSessionsConnectionEdges.model_rebuild()

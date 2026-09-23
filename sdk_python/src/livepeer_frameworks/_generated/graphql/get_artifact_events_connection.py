from pydantic import Field

from .base_model import BaseModel
from .fragments import ArtifactEventDefault, PageInfoDefault


class GetArtifactEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetArtifactEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetArtifactEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetArtifactEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetArtifactEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    artifact_events_connection: "GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnection" = Field(
        alias="artifactEventsConnection"
    )


class GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnection(BaseModel):
    edges: list[
        "GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionEdges"
    ]
    page_info: "GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionEdgesNode"
    )


GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionEdgesNode = (
    ArtifactEventDefault
)
GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionPageInfo = (
    PageInfoDefault
)
GetArtifactEventsConnection.model_rebuild()
GetArtifactEventsConnectionAnalytics.model_rebuild()
GetArtifactEventsConnectionAnalyticsLifecycle.model_rebuild()
GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnection.model_rebuild()
GetArtifactEventsConnectionAnalyticsLifecycleArtifactEventsConnectionEdges.model_rebuild()

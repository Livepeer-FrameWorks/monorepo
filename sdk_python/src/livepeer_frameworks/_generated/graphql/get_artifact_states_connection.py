from pydantic import Field

from .base_model import BaseModel
from .fragments import ArtifactStateDefault, PageInfoDefault


class GetArtifactStatesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetArtifactStatesConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetArtifactStatesConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetArtifactStatesConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetArtifactStatesConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    artifact_states_connection: "GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnection" = Field(
        alias="artifactStatesConnection"
    )


class GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnection(BaseModel):
    edges: list[
        "GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionEdges"
    ]
    page_info: "GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionEdges(
    BaseModel
):
    cursor: str
    node: (
        "GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionEdgesNode"
    )


GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionEdgesNode = (
    ArtifactStateDefault
)
GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionPageInfo = (
    PageInfoDefault
)
GetArtifactStatesConnection.model_rebuild()
GetArtifactStatesConnectionAnalytics.model_rebuild()
GetArtifactStatesConnectionAnalyticsLifecycle.model_rebuild()
GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnection.model_rebuild()
GetArtifactStatesConnectionAnalyticsLifecycleArtifactStatesConnectionEdges.model_rebuild()

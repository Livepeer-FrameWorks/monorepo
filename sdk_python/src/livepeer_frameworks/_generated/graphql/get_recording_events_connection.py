from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StreamEventDefault


class GetRecordingEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetRecordingEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetRecordingEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetRecordingEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetRecordingEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    recording_events_connection: "GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnection" = Field(
        alias="recordingEventsConnection",
        description="Latest recording outcomes and broadcast boundaries, without buffer/track noise.",
    )
    "Latest recording outcomes and broadcast boundaries, without buffer/track noise."


class GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnection(
    BaseModel
):
    edges: list[
        "GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionEdges"
    ]
    page_info: "GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionEdgesNode"


GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionEdgesNode = (
    StreamEventDefault
)
GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionPageInfo = (
    PageInfoDefault
)
GetRecordingEventsConnection.model_rebuild()
GetRecordingEventsConnectionAnalytics.model_rebuild()
GetRecordingEventsConnectionAnalyticsLifecycle.model_rebuild()
GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnection.model_rebuild()
GetRecordingEventsConnectionAnalyticsLifecycleRecordingEventsConnectionEdges.model_rebuild()

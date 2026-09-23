from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfoDefault, StorageEventDefault


class GetStorageEventsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    analytics: "GetStorageEventsConnectionAnalytics" = Field(
        description="Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."
    )
    "Unified analytics surface providing access to all platform metrics.\nCombines data from Periscope (historical) and Signalman (real-time)."


class GetStorageEventsConnectionAnalytics(BaseModel):
    """Unified analytics surface for all platform metrics.
    Organized into logical domains: usage, health, lifecycle, and infrastructure."""

    lifecycle: "GetStorageEventsConnectionAnalyticsLifecycle" = Field(
        description="Lifecycle analytics: stream events, artifacts, connections."
    )
    "Lifecycle analytics: stream events, artifacts, connections."


class GetStorageEventsConnectionAnalyticsLifecycle(BaseModel):
    """Lifecycle analytics for streams and artifacts.
    `streamId` arguments accept Stream.id (Relay global ID)."""

    storage_events_connection: "GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnection" = Field(
        alias="storageEventsConnection"
    )


class GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnection(BaseModel):
    edges: list[
        "GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionEdges"
    ]
    page_info: "GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionEdges(
    BaseModel
):
    cursor: str
    node: "GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionEdgesNode"


GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionEdgesNode = (
    StorageEventDefault
)
GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionPageInfo = (
    PageInfoDefault
)
GetStorageEventsConnection.model_rebuild()
GetStorageEventsConnectionAnalytics.model_rebuild()
GetStorageEventsConnectionAnalyticsLifecycle.model_rebuild()
GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnection.model_rebuild()
GetStorageEventsConnectionAnalyticsLifecycleStorageEventsConnectionEdges.model_rebuild()

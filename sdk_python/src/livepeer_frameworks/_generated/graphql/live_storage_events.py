from pydantic import Field

from .base_model import BaseModel
from .fragments import StorageEventDefault


class LiveStorageEvents(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_storage_events: "LiveStorageEventsLiveStorageEvents" = Field(
        alias="liveStorageEvents",
        description="Storage events for recordings and artifacts.\nOptionally filter by streamId.",
    )
    "Storage events for recordings and artifacts.\nOptionally filter by streamId."


LiveStorageEventsLiveStorageEvents = StorageEventDefault
LiveStorageEvents.model_rebuild()

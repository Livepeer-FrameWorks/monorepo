from pydantic import Field

from .base_model import BaseModel
from .fragments import ProcessingUsageRecordDefault


class LiveProcessingEvents(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    live_processing_events: "LiveProcessingEventsLiveProcessingEvents" = Field(
        alias="liveProcessingEvents",
        description="Transcoding and processing usage events.\nOptionally filter by streamId.",
    )
    "Transcoding and processing usage events.\nOptionally filter by streamId."


LiveProcessingEventsLiveProcessingEvents = ProcessingUsageRecordDefault
LiveProcessingEvents.model_rebuild()
